package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/ghodss/yaml"
	servicemanagement "google.golang.org/api/servicemanagement/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var (
	config       Config
	templatePath string
)

func init() {
	config = Config{
		Project:    "", // Derived from instance metadata server
		ProjectNum: "", // Derived from instance metadata server
	}

	if err := config.loadAndValidate(); err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
}

func main() {
	http.HandleFunc("/healthz", healthzHandler())
	http.HandleFunc("/", webhookHandler())

	log.Printf("[INFO] Initialized controller on port 80\n")
	log.Fatal(http.ListenAndServe(":80", nil))
}

func healthzHandler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "OK\n")
	}
}

func webhookHandler() func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "Unsupported method\n")
			return
		}

		var req SyncRequest
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&req); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("[ERROR] Could not parse SyncRequest: %v", err)
			return
		}

		desiredStatus, desiredChildren, err := sync(&req.Parent, &req.Children)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("[ERROR] Could not sync state: %v", err)
		}

		resp := SyncResponse{
			Status:   *desiredStatus,
			Children: *desiredChildren,
		}

		data, err := json.Marshal(resp)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			log.Printf("[ERROR] Could not generate SyncResponse: %v", err)
			return
		}
		fmt.Fprintf(w, string(data))
	}
}

func sync(parent *CloudEndpoint, children *CloudEndpointControllerRequestChildren) (*CloudEndpointControllerStatus, *[]interface{}, error) {
	status := makeStatus(parent, children)
	currState := status.StateCurrent
	log.Printf("[INFO] sync, %s - %s", parent.Spec.Target, currState)
	if currState == "" {
		currState = StateIdle
	}
	desiredChildren := make([]interface{}, 0)
	nextState := currState[0:1] + currState[1:] // string copy of currState

	changed := changeDetected(parent, children, status)

	if currState == StateIdle && changed {
		status.Endpoint = fmt.Sprintf("%s.endpoints.%s.cloud.goog", parent.Name, parent.Spec.Project)

		// Check if endpoint service exists, if not then create it.
		ep := status.Endpoint
		currService, err := config.clientServiceMan.Services.Get(ep).Do()
		if err != nil {
			if strings.Contains(err.Error(), "not found or permission denied") || (currService != nil && currService.HTTPStatusCode == 403) {
				log.Printf("[INFO][%s] Service does not yet exist, creating: %s", parent.Name, ep)
				_, err := config.clientServiceMan.Services.Create(&servicemanagement.ManagedService{
					ProducerProjectId: parent.Spec.Project,
					ServiceName:       ep,
				}).Do()
				if err != nil {
					return status, &desiredChildren, fmt.Errorf("[ERROR] Failed to creat Cloud Endpoints service: serviceName: %s, err: %v", ep, err)
				}
			} else {
				return status, &desiredChildren, fmt.Errorf("[ERROR][%s] Failed to get existing endpoint service: %v", parent.Name, err)
			}
		} else {
			log.Printf("[INFO][%s] Endpoint service already exists, skipping create.", parent.Name)
		}

		nextState = StateEndpointCreatePending

	}

	if currState == StateEndpointCreatePending {
		log.Printf("[INFO] Create pending: %s", parent.Name)
		var target string
		var openAPISpec string
		var err error
		openAPISpec, err = getUserSuppliedOpenAPISpec(parent)
		//log.Printf(openAPISpec)
		if err != nil {
			log.Printf("[INFO] failed to get user supplied spec")
			return status, &desiredChildren, err
		}
		target, status.JWTAudiences, err = getTarget(parent)
		if err != nil {
			log.Printf("[INFO] failed ot get target %s", err.Error())
			return status, &desiredChildren, err
		}
		if target != "" {
			log.Printf("target = %s", target)
			if openAPISpec == "" {
				// Create default spec

				openAPISpec, err = makeDefaultOpenAPISpec(status.Endpoint, target, status.JWTAudiences)
				if err != nil {
					return status, &desiredChildren, err
				}
				log.Printf("[INFO][%s] Target: %s", parent.Name, target)
				status.IngressIP = target
			} else {
				log.Printf("[INFO] Updating User Supplied API Spec")
				openAPISpec, err = executeTemplate(openAPISpec, status.Endpoint, target, status.JWTAudiences)
				if err := validateOpenAPISpec(openAPISpec); err != nil {
					return status, &desiredChildren, err
				}
			}
			// Submit endpoint config if service exists.
			ep := status.Endpoint
			_, err = config.clientServiceMan.Services.Get(ep).Do()
			if err != nil {
				log.Printf("[INFO][%s] Waiting for Endpoint creation: %s", parent.Name, ep)
				return status, &desiredChildren, nil
			}

			log.Printf("[INFO][%s] Endpoint created: %s, submitting endpoint config.", parent.Name, ep)

			configFiles := []*servicemanagement.ConfigFile{
				&servicemanagement.ConfigFile{
					FileContents: base64.StdEncoding.EncodeToString([]byte(openAPISpec)),
					FilePath:     "openapi.yaml",
					FileType:     "OPEN_API_YAML",
				},
			}

			req := servicemanagement.SubmitConfigSourceRequest{
				ValidateOnly: false,
				ConfigSource: &servicemanagement.ConfigSource{
					Files: configFiles,
				},
			}

			op, err := config.clientServiceMan.Services.Configs.Submit(ep, &req).Do()
			if err != nil {
				return status, &desiredChildren, fmt.Errorf("Failed to submit endpoint config: %v", err)
			}
			status.ConfigSubmit = op.Name

			nextState = StateEndpointSubmitPending

			status.LastAppliedSig = calcParentSig(parent, "")
		} else {
			log.Printf("[INFO][%s] target is nil", parent.Name)
		}
	}

	if currState == StateEndpointSubmitPending {
		ep := status.Endpoint
		opDone := true
		submitID := status.ConfigSubmit
		if submitID != "NA" {
			op, err := config.clientServiceMan.Operations.Get(submitID).Do()
			if err != nil {
				return status, &desiredChildren, fmt.Errorf("Failed to get service submit operation id: %s", status.ConfigSubmit)
			}
			opDone = op.Done

			var r servicemanagement.SubmitConfigSourceResponse
			data, _ := op.Response.MarshalJSON()
			if err := json.Unmarshal(data, &r); err != nil {
				return status, &desiredChildren, err
			}
			log.Printf("[INFO][%s] Service config submit complete for endpoint %s, config: %s", parent.Name, ep, r.ServiceConfig.Id)
			status.Config = r.ServiceConfig.Id
		}

		cfg := status.Config

		if opDone {
			found := false

			r, err := config.clientServiceMan.Services.Rollouts.List(ep).Do()
			if err != nil {
				return status, &desiredChildren, err
			}
			if len(r.Rollouts) > 0 {
				if _, ok := r.Rollouts[0].TrafficPercentStrategy.Percentages[cfg]; ok == true {
					log.Printf("[INFO][%s] Rollout for config already found, skipping rollout for endpoint: %s, config: %s", parent.Name, ep, cfg)
					status.ServiceRollout = "NA"
					found = true
				}
			}

			if found == false {
				// Rollout config
				log.Printf("[INFO][%s] Creating endpoint service config rollout for: endpoint: %s, config: %s", parent.Name, ep, cfg)

				op, err := config.clientServiceMan.Services.Rollouts.Create(ep, &servicemanagement.Rollout{
					TrafficPercentStrategy: &servicemanagement.TrafficPercentStrategy{
						Percentages: map[string]float64{
							cfg: 100.0,
						},
					},
				}).Do()
				if err != nil {
					return status, &desiredChildren, fmt.Errorf("Failed to create rollout for: endpoint: %s, config: %s", ep, cfg)
				}
				status.ServiceRollout = op.Name
			}
		}
		nextState = StateEndpointRolloutPending
	}

	if currState == StateEndpointRolloutPending {
		ep := status.Endpoint
		opName := status.ServiceRollout
		if opName != "NA" {
			op, err := config.clientServiceMan.Operations.Get(opName).Do()
			if err != nil {
				return status, &desiredChildren, err
			}
			if op.Done {
				cfg := status.Config
				log.Printf("[INFO][%s] Service config rollout complete for: endpoint: %s, config: %s", parent.Name, ep, cfg)
				nextState = StateIdle
			}
		}
	}

	// Advance the state
	if status.StateCurrent != nextState {
		log.Printf("[INFO][%s] Current state: %s", parent.Name, nextState)
	}
	status.StateCurrent = nextState

	return status, &desiredChildren, nil
}

func changeDetected(parent *CloudEndpoint, children *CloudEndpointControllerRequestChildren, status *CloudEndpointControllerStatus) bool {
	changed := false

	if status.StateCurrent == StateIdle {

		// Changed if parent spec changes
		if status.LastAppliedSig != calcParentSig(parent, "") {
			log.Printf("[DEBUG][%s] Changed because parent sig different", parent.Name)
			changed = true
		}

		// Changed if using target ingress and ingress IP changes.
		if parent.Spec.TargetIngress.Name != "" {
			// Fetch the ingress
			ingress, err := config.clientset.ExtensionsV1beta1().Ingresses(parent.Spec.TargetIngress.Namespace).Get(parent.Spec.TargetIngress.Name, metav1.GetOptions{})
			if err == nil {
				// Compare ingress IP with configured IP
				if len(ingress.Status.LoadBalancer.Ingress) > 0 && ingress.Status.LoadBalancer.Ingress[0].IP != status.IngressIP {
					log.Printf("[DEBUG][%s] Changed because ingress target IP changed", parent.Name)
					changed = true
				}
			}
		}
	}

	return changed
}
func getTarget(parent *CloudEndpoint) (string, []string, error) {
	var target string
	jwtAudiences := make([]string, 0)
	if parent.Spec.TargetIngress.Name != "" {
		// Fetch the ingress
		ingress, err := config.clientset.ExtensionsV1beta1().Ingresses(parent.Spec.TargetIngress.Namespace).Get(parent.Spec.TargetIngress.Name, metav1.GetOptions{})
		if err != nil {
			log.Printf("[INFO][%s] waiting for Ingress %s", parent.Name, parent.Spec.TargetIngress.Name)
			return "", nil, err
		}

		// Get target from ingress IP
		if len(ingress.Status.LoadBalancer.Ingress) < 1 {
			log.Printf("[INFO][%s] waiting for loadbalancer status from Ingress %s", parent.Name, parent.Spec.TargetIngress.Name)
			return "", nil, nil
		}
		target = ingress.Status.LoadBalancer.Ingress[0].IP

		// Populate the jwtAudiences
		if len(parent.Spec.TargetIngress.JWTServices) > 0 {
			ingBackends, err := getIngBackends(ingress)
			if err != nil {
				return "", nil, err
			}
			bePatterns := make([]string, len(parent.Spec.TargetIngress.JWTServices))

			for i, svcName := range parent.Spec.TargetIngress.JWTServices {
				svc, err := config.clientset.CoreV1().Services(parent.Spec.TargetIngress.Namespace).Get(svcName, metav1.GetOptions{})
				if err != nil {
					return "", nil, fmt.Errorf("Failed to populate JWT audience from kubernetes service, not found: '%s', %v", svcName, err)
				}
				if svc.Spec.Type == corev1.ServiceTypeNodePort && len(svc.Spec.Ports) > 0 {
					nodePort := strconv.Itoa(int(svc.Spec.Ports[0].NodePort))
					found := false
					for _, be := range ingBackends {
						if strings.Contains(be, fmt.Sprintf("k8s-be-%s", nodePort)) {
							bePatterns[i] = be
							backend, err := config.clientCompute.BackendServices.Get(config.Project, be).Do()
							if err == nil {
								found = true
								jwtAud := makeJWTAudience(config.ProjectNum, strconv.Itoa(int(backend.Id)))
								log.Printf("[INFO][%s] Created jwtAud: %s", parent.Name, jwtAud)
								jwtAudiences = append(jwtAudiences, jwtAud)
							}
						}
					}
					if found == false {
						return "", nil, fmt.Errorf("Backend not found or is not ready for service: %s, NodePort: %s", svcName, nodePort)
					}
				} else {
					return "", nil, fmt.Errorf("Service %s not type NodePort", svcName)
				}
			}
		}
	} else {
		target = parent.Spec.Target
	}
	return target, jwtAudiences, nil
}

func calcParentSig(parent *CloudEndpoint, addStr string) string {
	hasher := sha1.New()
	data, err := json.Marshal(&parent.Spec)
	if err != nil {
		log.Printf("[ERROR][%s] Failed to convert parent spec to JSON, this is a bug.", parent.Name)
		return ""
	}
	hasher.Write([]byte(data))
	hasher.Write([]byte(addStr))
	return fmt.Sprintf("%x", hasher.Sum(nil))
}

func validateOpenAPISpec(specOriginal string) (error) {
	var spec map[string]interface{}
	return yaml.Unmarshal([]byte(specOriginal), &spec)
}

func getUserSuppliedOpenAPISpec(parent *CloudEndpoint) (string, error) {
	var specString string
	var err error
	if parent.Spec.OpenAPISpec == nil {
		log.Printf("[DEBUG] %s Get user supplied API spec from ConfigMap", parent.Name)
		specString, err = getConfigMapSpec(parent)
	} else {
		log.Printf("[DEBUG] %s Get user supplied API spec from Spec", parent.Name)
		specYaml, err := yaml.Marshal(parent.Spec.OpenAPISpec)
		if err != nil {
			return "", err
		}
		specString = string(specYaml)
	}
	return specString, err
}

func getConfigMapSpec(parent *CloudEndpoint) (string, error) {
	var spec string
	var configMapSpec = parent.Spec.OpenAPISpecConfigMap
	var configMapName = configMapSpec.Name
	var configMapKey = configMapSpec.Key
	var err error
	if configMapName != "" && configMapKey != "" {
		log.Printf("[DEBUG] Looking for ConfigMap spec: %s, key: %s", configMapName, configMapKey)
		configMaps := config.clientset.CoreV1().ConfigMaps(parent.ObjectMeta.Namespace)
		configMap, err := configMaps.Get(configMapName, metav1.GetOptions{})
		if err == nil {
			spec = configMap.Data[configMapKey]
		} else {
			log.Printf("[ERROR][%s] error retrieving openapi spec from config map: %s.", configMapName, err.Error())
		}
	} else {
		if configMapName == "" {
			log.Printf("[DEBUG][%s] no ConfigMap Name", parent.Name)
		} else {
			log.Printf("[DEBUG][%s] no ConfigMap Key", parent.Name)
		}
	}
	return spec, err
}

func getWildcardAPITemplate(jwtAudiences []string) string {
	templateString := `
swagger: "2.0"
info:
  description: "wildcard config for any HTTP service."
  title: "General HTTP Service."
  version: "1.0.0"
host: "{{ .Endpoint }}"
x-google-endpoints:
- name: "{{ .Endpoint }}"
  target: "{{ .Target }}"
basePath: "/"
consumes:
- "application/json"
produces:
- "application/json"
schemes:
- "http"
- "https"
paths:
  "/**":
    get:
      operationId: Get
      responses:
        '200':
          description: Get
        default:
          description: Error
    delete:
      operationId: Delete
      responses:
        '204':
          description: Delete
        default:
          description: Error
    patch:
      operationId: Patch
      responses:
        '200':
          description: Patch
        default:
          description: Error
    post:
      operationId: Post
      responses:
        '200':
          description: Post
        default:
          description: Error
    put:
      operationId: Put
      responses:
        '200':
          description: Put
        default:
          description: Error
`
	templateJWTSecurityString := `
security:
- google_jwt: []
securityDefinitions:
  google_jwt:
    authorizationUrl: ""
    flow: "implicit"
    type: "oauth2"
    x-google-issuer: "https://cloud.google.com/iap"
    x-google-jwks_uri: "https://www.gstatic.com/iap/verify/public_key-jwk"
    x-google-audiences: "{{ StringsJoin .JWTAudiences "," }}"
`
	if jwtAudiences != nil {
		templateString = templateString + templateJWTSecurityString
	}
	return templateString
}

func executeTemplate(templateSpec, endpoint, target string, jwtAudiences []string) (string, error) {
	t, err := template.New("openapi.yaml").Funcs(template.FuncMap{"StringsJoin": strings.Join}).Parse(templateSpec)
	if err != nil {
		return "", err
	}
	type openAPISpecTemplateData struct {
		Endpoint     string
		Target       string
		JWTAudiences []string
	}

	data := openAPISpecTemplateData{
		Endpoint:     endpoint,
		Target:       target,
		JWTAudiences: jwtAudiences,
	}

	var b bytes.Buffer
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}

	return b.String(), nil
}

func makeDefaultOpenAPISpec(endpoint, target string, jwtAudiences []string) (string, error) {
	templateString := getWildcardAPITemplate(jwtAudiences)
	return executeTemplate(templateString, endpoint, target, jwtAudiences)
}

func getIngBackends(ing *v1beta1.Ingress) ([]string, error) {
	backends := make([]string, 0)

	if b, ok := ing.Annotations["ingress.kubernetes.io/backends"]; ok == true {
		var ingBackendsMap map[string]string
		if err := json.Unmarshal([]byte(b), &ingBackendsMap); err != nil {
			log.Printf("[WARN] Failed to parse ingress.kubernetes.io/backends annotation: %v", err)
			return backends, nil
		}
		for bs := range ingBackendsMap {
			backends = append(backends, bs)
		}
	}
	sort.Strings(backends)
	return backends, nil
}

func makeJWTAudience(projectNum, backendID string) string {
	return fmt.Sprintf("/projects/%s/global/backendServices/%s", projectNum, backendID)
}
