# Example Mutating Admission Webhook for Setting Pod HTTP Proxy

This work was cloned from [morvencao](https://github.com/morvencao/kube-mutating-webhook-tutorial)'s tutoral showing how to build and deploy a [MutatingAdmissionWebhook](https://kubernetes.io/docs/admin/admission-controllers/#mutatingadmissionwebhook-beta-in-19) that injects a set of env vars to allow dynamically setting HTTP Proxy in Pods versus using a [PodPreset](https://kubernetes.io/docs/concepts/workloads/pods/podpreset/).

## Prerequisites

Kubernetes 1.9.0 or above with the `admissionregistration.k8s.io/v1beta1` API enabled. Verify that by the following command:
```
kubectl api-versions | grep admissionregistration.k8s.io/v1beta1
```
The result should be:
```
admissionregistration.k8s.io/v1beta1
```

In addition, the `MutatingAdmissionWebhook` and `ValidatingAdmissionWebhook` admission controllers should be added and listed in the correct order in the admission-control flag of kube-apiserver.

## Build

1. Setup dep

   The repo uses [dep](https://github.com/golang/dep) as the dependency management tool for its Go codebase. Install `dep` by the following command:

```
go get -u github.com/golang/dep/cmd/dep
```

2. Build and push docker image

   Modify to push to your own registry :)

```
./build
```

## Deploy

1. Create a signed cert/key pair and store it in a Kubernetes `secret` that will be consumed by sidecar deployment

```
./deployment/webhook-create-signed-cert.sh \
    --service setenv-webhook-svc \
    --secret setenv-webhook-certs \
    --namespace default
```

2. Patch the `MutatingWebhookConfiguration` by set `caBundle` with correct value from Kubernetes cluster

```
cat deployment/mutatingwebhook.yaml | \
    deployment/webhook-patch-ca-bundle.sh > \
    deployment/mutatingwebhook-ca-bundle.yaml
```

3. Deploy resources

     Change the configmap variables to match your environment needs.

```
kubectl create -f deployment/configmap.yaml
kubectl create -f deployment/deployment.yaml
kubectl create -f deployment/service.yaml
kubectl create -f deployment/mutatingwebhook-ca-bundle.yaml
```

## Verify

1. The setenv webhook should be running

```
mg-imac:virtmerlin mglynn$ kubectl get pods
NAME                                         READY     STATUS        RESTARTS   AGE
setenv-webhook-deployment-69f77c8bb-m49zd    1/1       Running       0          16m
```

2. Deploy an app in the Kubernetes cluster, take `sleep` app as an example

```
cat <<EOF | kubectl create -f -
apiVersion: extensions/v1beta1
kind: Deployment
metadata:
  name: sleep
spec:
  replicas: 1
  template:
    metadata:
      labels:
        app: sleep
    spec:
      containers:
      - name: sleep
        image: tutum/curl
        command: ["/bin/sleep","infinity"]
EOF
```

3. Verify Variables Have Been Set

```
mg-imac:virtmerlin mglynn$ POD=$(kubectl get pod | grep sleep | awk '{print$1}') && kubectl exec -it $POD -- /bin/bash
root@sleep-7fb97b8788-v2zgl:/# set | grep HTTP
HTTPS_PROXY=https://USERNAME:PASSWORD@10.0.0.1:8080/
HTTP_PROXY=http://USERNAME:PASSWORD@10.0.1.1:8080/
KUBERNETES_SERVICE_PORT_HTTPS=443
```