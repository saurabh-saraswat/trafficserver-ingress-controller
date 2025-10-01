package watcher

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/apache/trafficserver-ingress-controller/endpoint"
	"github.com/apache/trafficserver-ingress-controller/proxy"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// newTestSniHandler creates a temporary AtsSniHandler for testing.
// It overrides FilePath to point to a temp sni.yaml file.
func newTestSniHandler(t *testing.T) (*AtsSniHandler, string) {
	tmpDir := t.TempDir()
	tmpFile := filepath.Join(tmpDir, "sni.yaml")

	// create empty file
	if err := os.WriteFile(tmpFile, []byte("sni:\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ep := createExampleEndpointWithFakeATSSni()
	h := NewAtsSniHandler("test-resource", &ep, tmpFile)
	return h, tmpFile
}

// newSniConfig creates an unstructured TrafficServerSNIConfig CR
// with the given fqdns.
func newSniConfig(name string, fqdns []string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "trafficserver.apache.org/v1alpha1",
			"kind":       "TrafficServerSNIConfig",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": "default",
			},
			"spec": map[string]interface{}{
				"sni": []interface{}{},
			},
		},
	}

	var rules []interface{}
	for _, fqdn := range fqdns {
		rules = append(rules, map[string]interface{}{
			"fqdn":                  fqdn,
			"verify_client":         "STRICT",
			"host_sni_policy":       "PERMISSIVE",
			"valid_tls_versions_in": []interface{}{"TLSv1_2", "TLSv1_3"},
		})
	}
	_ = unstructured.SetNestedSlice(u.Object, rules, "spec", "sni")
	return u
}

// TestAddSni verifies h.Add() adds fqdn entries into sni.yaml
func TestAddSni(t *testing.T) {
	h, tmpFile := newTestSniHandler(t)
	obj := newSniConfig("my-sni-config", []string{"ats.test.com", "host-test.com"})

	h.Add(obj)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read sni.yaml: %v", err)
	}
	content := string(data)
	if !contains(content, "ats.test.com") || !contains(content, "host-test.com") {
		t.Errorf("expected both fqdns, got: %s", content)
	}
}

// TestUpdateSni verifies h.Update() updates fqdn rules and removes old ones
func TestUpdateSni(t *testing.T) {
	h, tmpFile := newTestSniHandler(t)

	// Start with two fqdn entries
	oldObj := newSniConfig("my-sni-config", []string{"ats.test.com", "host-test.com"})
	h.Add(oldObj)

	// Update CRD: Update ats.test.com, keep host-test.com and add new fqdn
	newObj := newSniConfig("my-sni-config", []string{"ats.test.com", "new-host.com"})
	h.Update(oldObj, newObj)

	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read sni.yaml: %v", err)
	}
	content := string(data)

	// host-test.com should remain
	if !contains(content, "host-test.com") {
		t.Errorf("host-test.com remain, got: %s", content)
	}
	// ats.test.com should remain
	if !contains(content, "ats.test.com") {
		t.Errorf("ats.test.com should remain, got: %s", content)
	}
	// new fqdn should be added
	if !contains(content, "new-host.com") {
		t.Errorf("new-host.com should be present, got: %s", content)
	}
}

// TestDeleteSni verifies h.Delete() removes fqdn rules from sni.yaml
func TestDeleteSni(t *testing.T) {
	h, tmpFile := newTestSniHandler(t)

	// Start with multiple fqdns
	addObj := newSniConfig("my-sni-config", []string{"ats.test.com", "keep-me.com"})
	h.Add(addObj)

	// Delete CRD only containing ats.test.com
	delObj := newSniConfig("my-sni-config", []string{"ats.test.com"})
	h.Delete(delObj)

	// File should still exist
	if _, err := os.Stat(tmpFile); os.IsNotExist(err) {
		t.Errorf("Delete: expected sni.yaml to exist, but it was deleted")
	}

	// File should still contain keep-me.com but not ats.test.com
	data, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read sni.yaml: %v", err)
	}
	content := string(data)

	if contains(content, "ats.test.com") {
		t.Errorf("Delete: ats.test.com should have been removed, got: %s", content)
	}
	if !contains(content, "keep-me.com") {
		t.Errorf("Delete: keep-me.com should remain, got: %s", content)
	}

	// --- DELETE again to clear last fqdn ---
	delObj2 := newSniConfig("my-sni-config", []string{"keep-me.com"})
	h.Delete(delObj2)

	// Now file should be empty
	data, err = os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("failed to read sni.yaml: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("Delete: expected sni.yaml to be empty, got:\n%s", string(data))
	}
}

// TestLoadWriteSniFile verifies roundtrip of writeSniFile and loadSniFile
func TestLoadWriteSniFile(t *testing.T) {
	h, tmpFile := newTestSniHandler(t)

	expected := SniFile{
		Sni: []SniEntry{
			{"fqdn": "abc.com", "verify_client": "STRICT"},
		},
	}
	h.writeSniFile(expected)

	got := h.loadSniFile()
	if !reflect.DeepEqual(expected.Sni, got.Sni) {
		t.Errorf("expected %+v, got %+v", expected.Sni, got.Sni)
	}

	// empty file case
	h.writeSniFile(SniFile{})
	data, _ := os.ReadFile(tmpFile)
	if len(data) != 0 {
		t.Errorf("expected empty file, got: %s", string(data))
	}
}

// Utility contains check
func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (indexOf(s, sub) != -1)
}

func indexOf(str, substr string) int {
	for i := 0; i+len(substr) <= len(str); i++ {
		if str[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// Fake ATS Endpoint
func createExampleEndpointWithFakeATSSni() endpoint.Endpoint {
	return endpoint.Endpoint{
		ATSManager: &proxy.FakeATSManager{
			Namespace:    "default",
			IngressClass: "",
			Config:       make(map[string]string),
		},
	}
}
