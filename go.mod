module github.com/jlewi/cloud-endpoints-controller

go 1.13

require (
	cloud.google.com/go v0.57.0
	github.com/ghodss/yaml v0.0.0-20150909031657-73d445a93680
	golang.org/x/oauth2 v0.0.0-20200107190931-bf48bf16ab8d
	google.golang.org/api v0.25.0
	k8s.io/api v0.17.0
	k8s.io/apimachinery v0.17.1
	k8s.io/client-go v12.0.0+incompatible
	k8s.io/utils v0.0.0-20200529193333-24a76e807f40 // indirect
)

replace k8s.io/client-go => k8s.io/client-go v0.0.0-20190620085101-78d2af792bab
