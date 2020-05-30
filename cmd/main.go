package main

import (
	"github.com/jlewi/cloud-endpoints-controller/pkg"
	"log"
	"net/http"
)


func init() {
	pkg.ControllerConfig = pkg.Config{
		Project:    "", // Derived from instance metadata server
		ProjectNum: "", // Derived from instance metadata server
	}

	if err := pkg.ControllerConfig.LoadAndValidate(); err != nil {
		log.Fatalf("Error loading config: %v", err)
	}
}

func main() {
	http.HandleFunc("/healthz", pkg.HealthzHandler())
	http.HandleFunc("/", pkg.WebhookHandler())

	log.Printf("[INFO] Initialized controller on port 80\n")
	log.Fatal(http.ListenAndServe(":80", nil))
}

