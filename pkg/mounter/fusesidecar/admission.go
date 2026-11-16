package fusesidecar

import (
	"encoding/json"
	"net/http"

	admission "k8s.io/api/admission/v1"
	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type Handler struct {
	Client kubernetes.Interface
	Config Config
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var review admission.AdmissionReview
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&review); err != nil || review.Request == nil {
		http.Error(w, "invalid admission review", http.StatusBadRequest)
		return
	}
	req := review.Request
	resp := &admission.AdmissionResponse{UID: req.UID, Allowed: true}
	deny := func(message string) { resp.Allowed = false; resp.Result = &metav1.Status{Code: 422, Message: message} }
	if req.Operation == admission.Create && req.SubResource == "" && req.Resource.Group == "" && req.Resource.Resource == "pods" {
		var pod core.Pod
		if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
			deny("invalid Pod")
		} else {
			pod.Namespace = req.Namespace
			mutated, err := Mutate(r.Context(), h.Client, h.Config, &pod)
			if err != nil {
				deny(err.Error())
			} else {
				before, _ := json.Marshal(pod.Spec)
				after, _ := json.Marshal(mutated.Spec)
				if string(before) != string(after) {
					patch, err := json.Marshal([]map[string]any{{"op": "replace", "path": "/spec", "value": mutated.Spec}})
					if err != nil {
						deny("cannot encode Pod patch")
					} else {
						t := admission.PatchTypeJSONPatch
						resp.PatchType = &t
						resp.Patch = patch
					}
				}
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(admission.AdmissionReview{TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}, Response: resp})
}
