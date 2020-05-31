package pkg

import (
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"io/ioutil"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ghodss/yaml"
	rm "google.golang.org/api/cloudresourcemanager/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

// This file contains the code related to running in CLI mode. In CLI mode the binary is run locally
// and processes a file defining the cloud endpoints

// Process process the path containing a CloudEndpoints resource
// This is intended as the entrypoint when running in CLI mode.
func Process(path string, kubeContext string) error {
	endpoint := &CloudEndpoint{}

	data, err := ioutil.ReadFile(path)

	if err != nil {
		log.Printf("Error reading file: %v; error: %v", path, err)
		return err
	}

	if err := yaml.Unmarshal(data, endpoint); err != nil {
		log.Printf("Error unmarshaling %v; error: %v", path, err)
		return err
	}

	if err := ControllerConfig.initGcpClients(); err != nil {
		return err
	}

	ControllerConfig.Project = endpoint.Spec.Project

	log.Printf("Get Project number for project: %v", ControllerConfig.Project)

	clientScopes := []string{
		rm.CloudPlatformScope,
	}
	client, err := google.DefaultClient(oauth2.NoContext, strings.Join(clientScopes, " "))

	if err != nil {
		return err
	}

	rmClient, err := rm.New(client)

	if err != nil {
		log.Printf("Could not create a new resource manager service")
		return err
	}

	p, err := rmClient.Projects.Get(ControllerConfig.Project).Do()

	if err != nil {
		log.Printf("Error getting project: %v; error %v", ControllerConfig.Project, err)
		return err
	}

	ControllerConfig.ProjectNum = strconv.FormatInt(p.ProjectNumber, 10)
	log.Printf("Project %v has ProjectNumber %v ", ControllerConfig.Project, ControllerConfig.ProjectNum)

	kubeConfigFileName := kubeConfigPath()

	overrides := &clientcmd.ConfigOverrides{}

	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}

	config, err := clientcmd.LoadFromFile(kubeConfigFileName)

	if kubeContext != "" {
		config.CurrentContext = kubeContext
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeConfigFileName},
		overrides).ClientConfig()

	if err != nil {
		log.Printf("Could not load context: %v; from file %v; error: %v ",kubeContext, kubeConfigFileName, err)
		return err
	}

	log.Printf("Kubernetes host: %v", restConfig.Host)
	// create the clientset
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Printf("Could not create a K8s client; error %v", err)
		return err
	}

	ControllerConfig.clientset = clientset

	for ;; {
		status, _, err := sync(endpoint, &CloudEndpointControllerRequestChildren{})

		if err != nil {
			log.Printf("Error occured trying to sync endpoint; %v", err)
			return err
		}

		if status.StateCurrent == StateIdle {
			log.Printf("Reached IDLE state")
			return nil
		}

		endpoint.Status = *status
		time.Sleep(5* time.Second)
	}
	return nil
}

func kubeConfigPath() string {
	kubeconfigEnv := os.Getenv("KUBECONFIG")
	if kubeconfigEnv == "" {
		home := os.Getenv("HOMEDRIVE") + os.Getenv("HOMEPATH")
		if home == "" {
			for _, h := range []string{"HOME", "USERPROFILE"} {
				if home = os.Getenv(h); home != "" {
					break
				}
			}
		}
		kubeconfigPath := filepath.Join(home, ".kube", "config")
		return kubeconfigPath
	}
	return kubeconfigEnv
}