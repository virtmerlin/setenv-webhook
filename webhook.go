package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"

	"github.com/golang/glog"
	gopkgyaml "gopkg.in/yaml.v2"
	"k8s.io/api/admission/v1beta1"
	admissionregistrationv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/kubernetes/pkg/apis/core/v1"
)

var (
	runtimeScheme = runtime.NewScheme()
	codecs        = serializer.NewCodecFactory(runtimeScheme)
	deserializer  = codecs.UniversalDeserializer()
	// (https://github.com/kubernetes/kubernetes/issues/57982)
	defaulter = runtime.ObjectDefaulter(runtimeScheme)
)

var ignoredNamespaces = []string{
	metav1.NamespaceSystem,
	metav1.NamespacePublic,
}

const (
	admissionWebhookAnnotationInjectKey = "setenv.webhook.me/inject"
	admissionWebhookAnnotationStatusKey = "setenv.webhook.me/status"
)

type WebhookServer struct {
	//sidecarConfig *Config
	setenvConfig *setEnvConfig
	server       *http.Server
}

// Webhook Server parameters
type WhSvrParameters struct {
	port          int    // webhook server port
	certFile      string // path to the x509 certificate for https
	keyFile       string // path to the x509 private key matching `CertFile`
	setEnvCfgFile string // path to setenv configuration file
}

type Config struct {
	Containers []corev1.Container `yaml:"containers"`
	Volumes    []corev1.Volume    `yaml:"volumes"`
}

type setEnvConfig struct {
	EnvVars []corev1.EnvVar `yaml:"env"`
}

type patchOperation struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value,omitempty"`
}

///////////////
// functions //
///////////////

func init() {
	_ = corev1.AddToScheme(runtimeScheme)
	_ = admissionregistrationv1beta1.AddToScheme(runtimeScheme)
	// defaulting with webhooks:
	// https://github.com/kubernetes/kubernetes/issues/57982
	_ = v1.AddToScheme(runtimeScheme)
}

func loadSetEnvConfig(configFile string) (*setEnvConfig, error) {
	data, err := ioutil.ReadFile(configFile)
	if err != nil {
		return nil, err
	}

	var cfg setEnvConfig
	if err := gopkgyaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Check whether the target resoured need to be mutated
func mutationRequired(ignoredList []string, metadata *metav1.ObjectMeta) bool {
	// skip special kubernete system namespaces
	for _, namespace := range ignoredList {
		if metadata.Namespace == namespace {
			glog.Infof("Skip mutation for %v for it' in special namespace:%v", metadata.Name, metadata.Namespace)
			return false
		}
	}

	annotations := metadata.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}

	status := annotations[admissionWebhookAnnotationStatusKey]

	// determine whether to perform mutation based on annotation for the target resource
	var required bool = true
	if strings.ToLower(status) == "injected" {
		required = false
	}

	var pod_name string
	if metadata.Name == "" {
		pod_name = metadata.GenerateName
	} else {
		pod_name = metadata.Name
	}

	glog.Infof("Mutation policy for %v/%v: status: %q required:%v", metadata.Namespace, pod_name, status, required)
	return required
}

//////////////////////////////
///// Inject EnvVars  ////////
//////////////////////////////

func addEnv(target []corev1.Container, added []corev1.EnvVar) (patch []patchOperation) {

	var basePath string = "/spec/containers/"
	var addenvdef bool

	for x := 0; x < (len(target)); x++ {

		glog.Infof("Processing Container %v With Existing EnvVars:%v", x, target[x].Env)

		if target[x].Env == nil {
			addenvdef = true
		} else {
			addenvdef = false
		}

		var value interface{}
		path := basePath

		if addenvdef {
			path = path + strconv.Itoa(x) + "/env"
			value = added
			glog.Infof("No EnvVars Set ... adding array to PATH === %v  &&  VALUE =======:%v", path, value)
			patch = append(patch, patchOperation{
				Op:    "add",
				Path:  path,
				Value: value,
			})
		} else {
			path = path + strconv.Itoa(x) + "/env/-"
			for _, add := range added {
				value = add
				glog.Infof("Injecting PATH === %v  &&  VALUE =======:%v", path, value)
				patch = append(patch, patchOperation{
					Op:    "add",
					Path:  path,
					Value: value,
				})
			}
		}
	}
	return patch
}

func updateAnnotation(target map[string]string, added map[string]string) (patch []patchOperation) {
	for key, value := range added {
		if target == nil || target[key] == "" {
			target = map[string]string{}
			patch = append(patch, patchOperation{
				Op:   "add",
				Path: "/metadata/annotations",
				Value: map[string]string{
					key: value,
				},
			})
		} else {
			patch = append(patch, patchOperation{
				Op:    "replace",
				Path:  "/metadata/annotations/" + key,
				Value: value,
			})
		}
	}
	return patch
}

///////////////////////////////////////
// create mutation patch for resoures//
///////////////////////////////////////

func createPatch(pod *corev1.Pod, setenvConfig *setEnvConfig, annotations map[string]string) ([]byte, error) {

	var patch []patchOperation
	patch = append(patch, addEnv(pod.Spec.Containers, setenvConfig.EnvVars)...)
	patch = append(patch, updateAnnotation(pod.Annotations, annotations)...)

	return json.Marshal(patch)
}

///////////////////////////
// main mutation process //
///////////////////////////
func (whsvr *WebhookServer) mutate(ar *v1beta1.AdmissionReview) *v1beta1.AdmissionResponse {
	req := ar.Request
	var pod corev1.Pod

	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		glog.Errorf("Could not unmarshal raw object: %v", err)
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.Infof("AdmissionReview for Kind=%v, Namespace=%v Name=%v (%v) UID=%v patchOperation=%v UserInfo=%v",
		req.Kind, req.Namespace, req.Name, pod.Name, req.UID, req.Operation, req.UserInfo)

	// determine whether to perform mutation
	if !mutationRequired(ignoredNamespaces, &pod.ObjectMeta) {
		glog.Infof("Skipping mutation for %s/%s due to policy check", pod.Namespace, pod.Name)
		return &v1beta1.AdmissionResponse{
			Allowed: true,
		}
	}

	annotations := map[string]string{admissionWebhookAnnotationStatusKey: "injected"}
	patchBytes, err := createPatch(&pod, whsvr.setenvConfig, annotations)

	if err != nil {
		return &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	}

	glog.Infof("AdmissionResponse: patch=%v\n", string(patchBytes))
	return &v1beta1.AdmissionResponse{
		Allowed: true,
		Patch:   patchBytes,
		PatchType: func() *v1beta1.PatchType {
			pt := v1beta1.PatchTypeJSONPatch
			return &pt
		}(),
	}
}

// Serve method for webhook server
func (whsvr *WebhookServer) serve(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		if data, err := ioutil.ReadAll(r.Body); err == nil {
			body = data
		}
	}
	if len(body) == 0 {
		glog.Error("empty body")
		http.Error(w, "empty body", http.StatusBadRequest)
		return
	}

	// verify the content type is accurate
	contentType := r.Header.Get("Content-Type")
	if contentType != "application/json" {
		glog.Errorf("Content-Type=%s, expect application/json", contentType)
		http.Error(w, "invalid Content-Type, expect `application/json`", http.StatusUnsupportedMediaType)
		return
	}

	var admissionResponse *v1beta1.AdmissionResponse
	ar := v1beta1.AdmissionReview{}
	if _, _, err := deserializer.Decode(body, nil, &ar); err != nil {
		glog.Errorf("Can't decode body: %v", err)
		admissionResponse = &v1beta1.AdmissionResponse{
			Result: &metav1.Status{
				Message: err.Error(),
			},
		}
	} else {
		admissionResponse = whsvr.mutate(&ar)
	}

	admissionReview := v1beta1.AdmissionReview{}
	if admissionResponse != nil {
		admissionReview.Response = admissionResponse
		if ar.Request != nil {
			admissionReview.Response.UID = ar.Request.UID
		}
	}

	resp, err := json.Marshal(admissionReview)
	if err != nil {
		glog.Errorf("Can't encode response: %v", err)
		http.Error(w, fmt.Sprintf("could not encode response: %v", err), http.StatusInternalServerError)
	}
	glog.Infof("Ready to write reponse ...")
	if _, err := w.Write(resp); err != nil {
		glog.Errorf("Can't write response: %v", err)
		http.Error(w, fmt.Sprintf("could not write response: %v", err), http.StatusInternalServerError)
	}
}
