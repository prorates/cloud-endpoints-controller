## Development

This project uses the following build tools:

- [helm](https://helm.sh/)
- [dep](https://github.com/golang/dep)
- [skaffold](https://github.com/GoogleContainerTools/skaffold)
- [kustomize](https://github.com/kubernetes-sigs/kustomize)

1. Clone the repository into your `GOPATH`:

```
mkdir -p ${GOPATH}/src/github.com/danisla/
cd ${GOPATH}/src/github.com/danisla/
git clone https://github.com/danisla/cloud-endpoints-controller.git
```

Add your fork as another git remote:

```
FORK_URI=git@github.com:YOUR_GITHUB_USER/cloud-endpoints-controller.git
git remote add fork ${FORK_URI}
```

2. Modify the skaffold and kustomize image to a docker registry you can push to:

In skaffold.yaml:

```
build:
  artifacts:
  - imageName: YOUR_REGISTRY/cloud-endpoints-controller
```

> Replace `YOUR_REGISTRY` with something you can push to. 

In `manifests/dev/image.yaml`:

```
spec:
  template:
    spec:
      containers:
      - name: cloud-endpoints-controller
        image: YOUR_REGISTRY/cloud-endpoints-controller
```

> Replace `YOUR_REGISTRY` with something you can push to.

3. Install the metacontroller:

```
make install-metacontroller
```

4. Install go dependencies:

```
dep ensure
```

5. Run in cluster with skaffold:

```
skaffold dev
```

## Testing

1. Run all tests:

```
make test
```

2. Stop tests:

```
make test-stop
```

## Building the release container image

1. Build image using container builder in current project:

```
make image
```

## Submitting a pull request

1. Push changes to a branch in your fork.

```
git checkout -b my-new-feature
git add .
git commit -m "my new feature"
git push fork my-new-feature
```

2. Submit a Github pull request from your branch in your fork to the master branch.
