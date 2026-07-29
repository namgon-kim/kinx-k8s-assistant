package contract

import "testing"

type cloneDataStruct struct {
	Labels   map[string]string
	Items    []cloneDataItem
	Metadata *cloneDataMetadata
}

type cloneDataItem struct {
	Values []string
}

type cloneDataMetadata struct {
	Flags map[string]bool
}

func TestCloneDataMapDeepCopiesTypedNestedCollections(t *testing.T) {
	source := map[string]any{
		"objects": []map[string]any{{
			"labels": map[string]string{"app": "web"},
			"flags":  map[string]bool{"ready": true},
		}},
	}

	cloned := CloneDataMap(source)
	objects := cloned["objects"].([]map[string]any)
	objects[0]["labels"].(map[string]string)["app"] = "changed"
	objects[0]["flags"].(map[string]bool)["ready"] = false

	original := source["objects"].([]map[string]any)[0]
	if original["labels"].(map[string]string)["app"] != "web" {
		t.Fatal("typed nested map shared storage with clone")
	}
	if !original["flags"].(map[string]bool)["ready"] {
		t.Fatal("typed nested bool map shared storage with clone")
	}
}

func TestCloneDataMapDeepCopiesStructReferenceFields(t *testing.T) {
	source := map[string]any{
		"content": cloneDataStruct{
			Labels: map[string]string{"app": "web"},
			Items: []cloneDataItem{{
				Values: []string{"ready"},
			}},
			Metadata: &cloneDataMetadata{
				Flags: map[string]bool{"healthy": true},
			},
		},
	}

	cloned := CloneDataMap(source)
	content := cloned["content"].(cloneDataStruct)
	content.Labels["app"] = "changed"
	content.Items[0].Values[0] = "failed"
	content.Metadata.Flags["healthy"] = false

	original := source["content"].(cloneDataStruct)
	if original.Labels["app"] != "web" {
		t.Fatal("struct map field shared storage with clone")
	}
	if original.Items[0].Values[0] != "ready" {
		t.Fatal("struct slice field shared storage with clone")
	}
	if !original.Metadata.Flags["healthy"] {
		t.Fatal("struct pointer field shared storage with clone")
	}
}
