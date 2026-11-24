package state

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestGetAllRoutedHosts_SortedAndDeduplicated(t *testing.T) {
	mgr := NewManager(filepath.Join(t.TempDir(), "state.yaml"))
	if err := mgr.RegisterProjectWithRouting("beta", "/tmp/beta", nil, nil, []string{"beta.0ploy.dev", "shared.example.test"}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.RegisterProjectWithRouting("alpha", "/tmp/alpha", nil, nil, []string{"shared.example.test", "alpha.0ploy.dev"}); err != nil {
		t.Fatal(err)
	}

	got, err := mgr.GetAllRoutedHosts()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha.0ploy.dev", "beta.0ploy.dev", "shared.example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GetAllRoutedHosts() = %v, want %v", got, want)
	}
}
