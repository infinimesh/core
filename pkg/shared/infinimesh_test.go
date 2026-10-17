package infinimesh

import "testing"

func TestContextKeys(t *testing.T) {
	keys := []ContextKey{
		InfinimeshRootCtxKey,
		InfinimeshAccountCtxKey,
		InfinimeshSessionCtxKey,
		InfinimeshDevicesCtxKey,
		InfinimeshStreamCtxKey,
		InfinimeshNamespaceCtxKey,
		InfinimeshPostCtxKey,
		InfinimeshJTICtxKey,
		InfinimeshPluginCtxKey,
		InfinimeshOTAProviderCtxKey,
	}
	want := []string{
		INFINIMESH_ROOT_CLAIM,
		INFINIMESH_ACCOUNT_CLAIM,
		INFINIMESH_SESSION_CLAIM,
		INFINIMESH_DEVICES_CLAIM,
		INFINIMESH_STREAM_CLAIM,
		INFINIMESH_NAMESPACE_CLAIM,
		INFINIMESH_POST_CLAIM,
		INFINIMESH_JTI_CLAIM,
		INFINIMESH_PLUGIN_CLAIM,
		INFINIMESH_OTA_PROVIDER_CLAIM,
	}
	if INFINIMESH_NOSESSION_CLAIM == "" {
		t.Fatal("empty nosession claim")
	}
	for i, k := range keys {
		if string(k) != want[i] {
			t.Fatalf("%s != %s", k, want[i])
		}
	}
}
