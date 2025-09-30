package watcher

import (
	"log"
	"os"
	"reflect"

	"github.com/apache/trafficserver-ingress-controller/endpoint"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// AtsSniHandler handles TrafficServerSNIConfig CR events
type AtsSniHandler struct {
	ResourceName string
	Ep           *endpoint.Endpoint
	FilePath     string
}

// Constructor
func NewAtsSniHandler(resource string, ep *endpoint.Endpoint, path string) *AtsSniHandler {
	log.Println("Ats SNI Handler initialized")
	return &AtsSniHandler{ResourceName: resource, Ep: ep, FilePath: path}
}

// SniEntry represents one fqdn entry in sni.yaml
type SniEntry map[string]interface{}

// SniFile represents the full sni.yaml structure
type SniFile struct {
	Sni []SniEntry `yaml:"sni,omitempty"`
}

// Add handles creation of TrafficServerSNIConfig
func (h *AtsSniHandler) Add(obj interface{}) {
	u := obj.(*unstructured.Unstructured)
	log.Printf("[ADD] TrafficServerSNIConfig: %s/%s", u.GetNamespace(), u.GetName())

	newSni, found, err := unstructured.NestedSlice(u.Object, "spec", "sni")
	if err != nil || !found {
		log.Printf("Add: sni not found or error: %v", err)
		return
	}

	// Load existing sni.yaml
	sniFile := h.loadSniFile()

	// Merge entries
	for _, entry := range newSni {
		entryMap, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		fqdn, ok := entryMap["fqdn"].(string)
		if !ok || fqdn == "" {
			continue
		}

		updated := false
		for i, existing := range sniFile.Sni {
			if existingFqdn, _ := existing["fqdn"].(string); existingFqdn == fqdn {
				if !reflect.DeepEqual(existing, entryMap) {
					sniFile.Sni[i] = entryMap
				}
				updated = true
				break
			}
		}
		if !updated {
			sniFile.Sni = append(sniFile.Sni, entryMap)
		}
	}

	h.writeSniFile(sniFile)
	h.reloadSni()
}

// Update handles updates of TrafficServerSNIConfig
func (h *AtsSniHandler) Update(oldObj, newObj interface{}) {
	oldU := oldObj.(*unstructured.Unstructured)
	newU := newObj.(*unstructured.Unstructured)
	log.Printf("[UPDATE] TrafficServerSNIConfig: %s/%s", newU.GetNamespace(), newU.GetName())

	oldSni, _, _ := unstructured.NestedSlice(oldU.Object, "spec", "sni")
	newSni, found, err := unstructured.NestedSlice(newU.Object, "spec", "sni")
	if err != nil || !found {
		log.Printf("Update: sni not found or error: %v", err)
		return
	}

	sniFile := h.loadSniFile()

	// Remove entries present in old but missing in new
	oldMap := make(map[string]SniEntry)
	for _, entry := range oldSni {
		if m, ok := entry.(map[string]interface{}); ok {
			if fqdn, ok := m["fqdn"].(string); ok && fqdn != "" {
				oldMap[fqdn] = m
			}
		}
	}
	newMap := make(map[string]SniEntry)
	for _, entry := range newSni {
		if m, ok := entry.(map[string]interface{}); ok {
			if fqdn, ok := m["fqdn"].(string); ok && fqdn != "" {
				newMap[fqdn] = m
			}
		}
	}

	// Remove deleted fqdn entries
	var updatedSni []SniEntry
	for _, existing := range sniFile.Sni {
		fqdn := existing["fqdn"].(string)
		if _, existsInOld := oldMap[fqdn]; existsInOld {
			if _, existsInNew := newMap[fqdn]; !existsInNew {
				continue // remove entry
			}
		}
		updatedSni = append(updatedSni, existing)
	}

	// Add/update new entries
	for fqdn, newEntry := range newMap {
		found := false
		for i, existing := range updatedSni {
			if existingFqdn, _ := existing["fqdn"].(string); existingFqdn == fqdn {
				if !reflect.DeepEqual(existing, newEntry) {
					updatedSni[i] = newEntry
				}
				found = true
				break
			}
		}
		if !found {
			updatedSni = append(updatedSni, newEntry)
		}
	}

	sniFile.Sni = updatedSni
	h.writeSniFile(sniFile)
	h.reloadSni()
}

// Delete handles deletion of TrafficServerSNIConfig
func (h *AtsSniHandler) Delete(obj interface{}) {
	u := obj.(*unstructured.Unstructured)
	log.Printf("[DELETE] TrafficServerSNIConfig: %s/%s", u.GetNamespace(), u.GetName())

	// Empty the file contents but do not delete the file
	err := os.WriteFile(h.FilePath, []byte(""), 0644)
	if err != nil {
		log.Printf("failed to clear sni.yaml: %v", err)
	}

	// Trigger ATS reload
	h.reloadSni()
}

// loadSniFile reads existing sni.yaml
func (h *AtsSniHandler) loadSniFile() SniFile {
	var sniFile SniFile
	data, err := os.ReadFile(h.FilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return sniFile
		}
		log.Printf("Failed to read sni.yaml: %v", err)
		return sniFile
	}
	if err := yaml.Unmarshal(data, &sniFile); err != nil {
		log.Printf("Failed to unmarshal sni.yaml: %v", err)
	}
	return sniFile
}

// writeSniFile writes sni.yaml with atomic overwrite
func (h *AtsSniHandler) writeSniFile(sniFile SniFile) {
	if len(sniFile.Sni) == 0 {
		if err := os.WriteFile(h.FilePath, []byte{}, 0644); err != nil {
			log.Printf("Failed to clear sni.yaml: %v", err)
		}
		return
	}
	data, err := yaml.Marshal(&sniFile)
	if err != nil {
		log.Printf("Failed to marshal sni.yaml: %v", err)
		return
	}
	tmp := h.FilePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		log.Printf("Failed to write temp sni.yaml: %v", err)
		return
	}
	_ = os.Rename(tmp, h.FilePath)
}

// reloadSni triggers ATS to reload the sni.yaml
func (h *AtsSniHandler) reloadSni() {
	if h.Ep != nil && h.Ep.ATSManager != nil {
		if msg, err := h.Ep.ATSManager.SniSet(); err != nil {
			log.Printf("Failed to reload ATS SNI: %v", err)
		} else {
			log.Printf("ATS SNI reloaded: %s", msg)
		}
	}
}
