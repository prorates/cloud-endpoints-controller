package main

import (
	"flag"
	"github.com/jlewi/cloud-endpoints-controller/pkg"
	"log"
	"net/http"
)

type options struct {
	path string
	context string
}

// AddFlags adds flags for a specific Server to the specified FlagSet
func (o *options) AddFlags(fs *flag.FlagSet) {
	fs.StringVar(&o.path, "f", "", "The path of the file to process; if not set run in webhook mode")
	fs.StringVar(&o.context, "context", "", "The kubernetes context to use; if not set uses the current context")
}

func main() {
	o := &options{}

	o.AddFlags(flag.CommandLine)
	flag.Parse()

	if o.path == "" {
		log.Printf("Running in webhook mode")
		pkg.ControllerConfig = pkg.Config{
			Project:    "", // Derived from instance metadata server
			ProjectNum: "", // Derived from instance metadata server
		}

		if err := pkg.ControllerConfig.LoadAndValidateControllerConfig(); err != nil {
			log.Fatalf("Error loading config: %v", err)
		}

		log.Printf("Running in WebHook Mode")
		http.HandleFunc("/healthz", pkg.HealthzHandler())
		http.HandleFunc("/", pkg.WebhookHandler())

		log.Printf("[INFO] Initialized controller on port 80\n")
		log.Fatal(http.ListenAndServe(":80", nil))
	} else {
		log.Printf("Running in CLI Mode")
		if err := pkg.Process(o.path, o.context); err != nil {
			log.Fatalf("Error occurred processing; %v; error: %v", o.path, err)
		}
	}
}

