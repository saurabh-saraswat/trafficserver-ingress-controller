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
	newU := newObj.(*unstructured.Unstructured)
	log.Printf("[UPDATE] TrafficServerSNIConfig: %s/%s", newU.GetNamespace(), newU.GetName())

	newSni, found, err := unstructured.NestedSlice(newU.Object, "spec", "sni")
	if err != nil || !found {
		log.Printf("Update: sni not found or error: %v", err)
		return
	}

	sniFile := h.loadSniFile()

	// Build a map for quick fqdn lookup from new object
	newMap := make(map[string]SniEntry)
	for _, entry := range newSni {
		if m, ok := entry.(map[string]interface{}); ok {
			if fqdn, ok := m["fqdn"].(string); ok && fqdn != "" {
				newMap[fqdn] = m
			}
		}
	}

	log.Println("New Updated map in Update function ", newMap)
	var updatedSni []SniEntry
	seen := make(map[string]struct{})

	// Update existing file entries if fqdn matches
	for _, existing := range sniFile.Sni {
		fqdn, _ := existing["fqdn"].(string)
		if newEntry, ok := newMap[fqdn]; ok {
			// Update with new entry
			updatedSni = append(updatedSni, newEntry)
			seen[fqdn] = struct{}{}
		} else {
			// Keep old entry if not in new CRD
			updatedSni = append(updatedSni, existing)
		}
	}

	log.Println("Update sni without adding new entries ", updatedSni)

	// Add any new fqdn not already in the file
	for fqdn, newEntry := range newMap {
		if _, already := seen[fqdn]; !already {
			updatedSni = append(updatedSni, newEntry)
		}
	}

	log.Println("Updated sni with new entries ", updatedSni)

	sniFile.Sni = updatedSni
	h.writeSniFile(sniFile)
	h.reloadSni()
}

func (h *AtsSniHandler) Delete(obj interface{}) {
	u := obj.(*unstructured.Unstructured)
	log.Printf("[DELETE] TrafficServerSNIConfig: %s/%s", u.GetNamespace(), u.GetName())

	sniFile := h.loadSniFile()

	// Get fqdn list from deleted object
	sniList, found, err := unstructured.NestedSlice(u.Object, "spec", "sni")
	if err != nil || !found {
		log.Printf("Delete: sni not found or error: %v", err)
		return
	}

	delMap := make(map[string]struct{})
	for _, entry := range sniList {
		if m, ok := entry.(map[string]interface{}); ok {
			if fqdn, ok := m["fqdn"].(string); ok && fqdn != "" {
				delMap[fqdn] = struct{}{}
			}
		}
	}

	log.Println("Entries to be deleted ", delMap)

	// Keep only those not in delMap
	var updatedSni []SniEntry
	for _, existing := range sniFile.Sni {
		fqdn, _ := existing["fqdn"].(string)
		if _, toDelete := delMap[fqdn]; !toDelete {
			log.Println("to not delete: ", existing)
			updatedSni = append(updatedSni, existing)
		}
	}

	log.Println("In delete final updated sni", updatedSni)
	sniFile.Sni = updatedSni
	h.writeSniFile(sniFile)
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
