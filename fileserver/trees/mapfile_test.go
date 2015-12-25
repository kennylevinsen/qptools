package trees

import (
	"testing"

	"github.com/joushou/qp"
)

func TestMapFileBulkWrite(t *testing.T) {
	mf := NewMapFile("test", 0777, "test", "test")
	mfh1, err := mf.Open("test", qp.ORDWR)
	if err != nil {
		t.Errorf("open failed: %v", err)
	}

	mfh1.Write([]byte("key1=value1\nkey2=value2\nkey3=value3\n"))
	var key1, key2, key3 bool
	for key := range mf.store {
		switch key {
		case "key1":
			key1 = true
			if mf.store[key] != "value1" {
				t.Errorf("key1 contained %s, expected value1", mf.store[key])
			}
		case "key2":
			key2 = true
			if mf.store[key] != "value2" {
				t.Errorf("key2 contained %s, expected value2", mf.store[key])
			}
		case "key3":
			key3 = true
			if mf.store[key] != "value3" {
				t.Errorf("key3 contained %s, expected value3", mf.store[key])
			}
		default:
			t.Errorf("map contained erroneous key: %s", key)
		}
	}
	if !(key1 && key2 && key3) {
		t.Errorf("key missing from map")
	}
}

func TestMapFileOverride(t *testing.T) {
	mf := NewMapFile("test", 0777, "test", "test")
	mfh1, err := mf.Open("test", qp.ORDWR)
	if err != nil {
		t.Errorf("open failed: %v", err)
	}

	mfh1.Write([]byte("key1=value1\nkey2=value2\n"))
	var key1, key2 bool
	for key := range mf.store {
		switch key {
		case "key1":
			key1 = true
			if mf.store[key] != "value1" {
				t.Errorf("key1 contained %s, expected value1", mf.store[key])
			}
		case "key2":
			key2 = true
			if mf.store[key] != "value2" {
				t.Errorf("key2 contained %s, expected value2", mf.store[key])
			}
		default:
			t.Errorf("map contained erroneous key: %s", key)
		}
	}
	if !(key1 && key2) {
		t.Errorf("key missing from map")
	}

	mfh1.Write([]byte("key1=value3\n"))
	key1, key2 = false, false
	for key := range mf.store {
		switch key {
		case "key1":
			key1 = true
			if mf.store[key] != "value3" {
				t.Errorf("key1 contained %s, expected value3", mf.store[key])
			}
		case "key2":
			key2 = true
			if mf.store[key] != "value2" {
				t.Errorf("key2 contained %s, expected value2", mf.store[key])
			}
		default:
			t.Errorf("map contained erroneous key: %s", key)
		}
	}
	if !(key1 && key2) {
		t.Errorf("key missing from map")
	}

	mfh1.Write([]byte("key2=value4\n"))
	key1, key2 = false, false
	for key := range mf.store {
		switch key {
		case "key1":
			key1 = true
			if mf.store[key] != "value3" {
				t.Errorf("key1 contained %s, expected value3", mf.store[key])
			}
		case "key2":
			key2 = true
			if mf.store[key] != "value4" {
				t.Errorf("key2 contained %s, expected value4", mf.store[key])
			}
		default:
			t.Errorf("map contained erroneous key: %s", key)
		}
	}
	if !(key1 && key2) {
		t.Errorf("key missing from map")
	}
}

func TestMapFileDelete(t *testing.T) {
	mf := NewMapFile("test", 0777, "test", "test")
	mfh1, err := mf.Open("test", qp.ORDWR)
	if err != nil {
		t.Errorf("open failed: %v", err)
	}

	mfh1.Write([]byte("key1=value1\nkey2=value2\n"))
	var key1, key2 bool
	for key := range mf.store {
		switch key {
		case "key1":
			key1 = true
		case "key2":
			key2 = true
		}
	}
	if !(key1 && key2) {
		t.Errorf("key missing from map")
	}

	mfh1.Write([]byte("key1=\n"))
	key1, key2 = false, false
	for key := range mf.store {
		switch key {
		case "key1":
			t.Errorf("key1 exists in map with content %s, expected it to be deleted", mf.store[key])
		case "key2":
			key2 = true
		}
	}
	if !key2 {
		t.Errorf("key missing from map")
	}
}
