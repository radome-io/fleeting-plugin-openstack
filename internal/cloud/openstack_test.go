package cloud

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient returns a client served by mux under /compute/, /image/ and
// /network/.
func newTestClient(t *testing.T, mux *http.ServeMux) *openStack {
	t.Helper()

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	service := func(path string) *gophercloud.ServiceClient {
		return &gophercloud.ServiceClient{
			ProviderClient: &gophercloud.ProviderClient{TokenID: "token"},
			Endpoint:       server.URL + path,
		}
	}

	return &openStack{
		region:  "RegionOne",
		nova:    service("/compute/"),
		glance:  service("/image/"),
		neutron: service("/network/"),
	}
}

func respond(t *testing.T, w http.ResponseWriter, status int, body string) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err := io.WriteString(w, body)
	assert.NoError(t, err)
}

func TestListServers(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /compute/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "fleeting:runner", r.URL.Query().Get("tags"))

		respond(t, w, http.StatusOK, `{"servers": [
			{
				"id": "a", "name": "runner-abcdefgh", "status": "ACTIVE",
				"OS-EXT-STS:task_state": null,
				"created": "2026-11-09T12:00:00Z",
				"image": {"id": "image-ubuntu", "links": []},
				"addresses": {
					"private": [{"addr": "10.0.0.5", "version": 4, "OS-EXT-IPS:type": "fixed"},
					            {"addr": "198.51.100.7", "version": 4, "OS-EXT-IPS:type": "floating"}],
					"public": [{"addr": "203.0.113.10", "version": 4, "OS-EXT-IPS:type": "fixed"},
					            {"addr": "2001:db8::10", "version": 6, "OS-EXT-IPS:type": "fixed"}]
				}
			},
			{
				"id": "b", "name": "runner-ijklmnop", "status": "ERROR",
				"OS-EXT-STS:task_state": "deleting",
				"image": "",
				"fault": {"code": 500, "message": "No valid host was found."},
				"addresses": {}
			}
		]}`)
	})

	found, err := newTestClient(t, mux).ListServers(t.Context(), "fleeting:runner")

	require.NoError(t, err)
	assert.Equal(t, []Server{
		{
			ID:      "a",
			Name:    "runner-abcdefgh",
			Status:  "ACTIVE",
			Created: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
			ImageID: "image-ubuntu",
			Addresses: []Address{
				{Network: "private", IP: "10.0.0.5", Version: 4},
				{Network: "private", IP: "198.51.100.7", Version: 4, Floating: true},
				{Network: "public", IP: "203.0.113.10", Version: 4},
				{Network: "public", IP: "2001:db8::10", Version: 6},
			},
		},
		{
			ID:        "b",
			Name:      "runner-ijklmnop",
			Status:    "ERROR",
			TaskState: "deleting",
			Fault:     "No valid host was found.",
		},
	}, found)
}

func TestGetServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /compute/servers/a", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"server": {"id": "a", "status": "BUILD", "image": {"id": "image-ubuntu"}}}`)
	})
	mux.HandleFunc("GET /compute/servers/gone", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusNotFound, `{"itemNotFound": {"code": 404}}`)
	})
	mux.HandleFunc("GET /compute/servers/broken", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusInternalServerError, `{}`)
	})
	client := newTestClient(t, mux)

	s, err := client.GetServer(t.Context(), "a")
	require.NoError(t, err)
	assert.Equal(t, Server{ID: "a", Status: "BUILD", ImageID: "image-ubuntu"}, s)

	_, err = client.GetServer(t.Context(), "gone")
	require.ErrorIs(t, err, ErrNotFound)

	_, err = client.GetServer(t.Context(), "broken")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotFound)
}

func TestCreateServer(t *testing.T) {
	tests := []struct {
		name string
		req  CreateRequest
		body map[string]any
	}{
		{
			name: "every setting",
			req: CreateRequest{
				Name:             "runner-abcdefgh",
				FlavorID:         "flavor-large",
				ImageID:          "image-ubuntu",
				NetworkIDs:       []string{"network-public"},
				SecurityGroupIDs: []string{"group-ssh"},
				KeyName:          "runner",
				UserData:         "#cloud-config\n",
				ConfigDrive:      true,
				Metadata:         map[string]string{"purpose": "ci"},
				Tags:             []string{"fleeting:runner"},
				AvailabilityZone: "nova",
				ServerGroupID:    "8f3a9c1e-4b2d-4e6f-9a7b-1c2d3e4f5a6b",
			},
			body: map[string]any{
				"os:scheduler_hints": map[string]any{"group": "8f3a9c1e-4b2d-4e6f-9a7b-1c2d3e4f5a6b"},
				"server": map[string]any{
					"name":              "runner-abcdefgh",
					"flavorRef":         "flavor-large",
					"imageRef":          "image-ubuntu",
					"networks":          []any{map[string]any{"uuid": "network-public"}},
					"security_groups":   []any{map[string]any{"name": "group-ssh"}},
					"key_name":          "runner",
					"user_data":         base64.StdEncoding.EncodeToString([]byte("#cloud-config\n")),
					"config_drive":      true,
					"metadata":          map[string]any{"purpose": "ci"},
					"tags":              []any{"fleeting:runner"},
					"availability_zone": "nova",
				},
			},
		},
		{
			name: "defaults",
			req: CreateRequest{
				Name:       "runner-abcdefgh",
				FlavorID:   "flavor-large",
				ImageID:    "image-ubuntu",
				NetworkIDs: []string{"network-public"},
			},
			body: map[string]any{
				"server": map[string]any{
					"name":      "runner-abcdefgh",
					"flavorRef": "flavor-large",
					"imageRef":  "image-ubuntu",
					"networks":  []any{map[string]any{"uuid": "network-public"}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any

			mux := http.NewServeMux()
			mux.HandleFunc("POST /compute/servers", func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				respond(t, w, http.StatusAccepted, `{"server": {"id": "new"}}`)
			})

			id, err := newTestClient(t, mux).CreateServer(t.Context(), tt.req)

			require.NoError(t, err)
			assert.Equal(t, "new", id)
			assert.Equal(t, tt.body, body)
		})
	}
}

func TestCreateServerUserData(t *testing.T) {
	tests := []struct {
		name     string
		userData string
		sent     any
	}{
		{name: "none", userData: "", sent: nil},
		{name: "already valid base64", userData: "aGVsbG8=", sent: base64.StdEncoding.EncodeToString([]byte("aGVsbG8="))},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]map[string]any

			mux := http.NewServeMux()
			mux.HandleFunc("POST /compute/servers", func(w http.ResponseWriter, r *http.Request) {
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				respond(t, w, http.StatusAccepted, `{"server": {"id": "new"}}`)
			})

			_, err := newTestClient(t, mux).CreateServer(t.Context(), CreateRequest{Name: "a", UserData: tt.userData})

			require.NoError(t, err)
			assert.Equal(t, tt.sent, body["server"]["user_data"])
		})
	}
}

func TestDeleteServer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /compute/servers/a", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /compute/servers/gone", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusNotFound, `{}`)
	})
	mux.HandleFunc("DELETE /compute/servers/locked", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusConflict, `{}`)
	})
	client := newTestClient(t, mux)

	require.NoError(t, client.DeleteServer(t.Context(), "a"))
	require.NoError(t, client.DeleteServer(t.Context(), "gone"))
	require.Error(t, client.DeleteServer(t.Context(), "locked"))
}

func TestFindFlavor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /compute/flavors/detail", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"flavors": [
			{"id": "flavor-large", "name": "m1.large"},
			{"id": "flavor-twin-1", "name": "twin"},
			{"id": "flavor-twin-2", "name": "twin"}
		]}`)
	})
	client := newTestClient(t, mux)

	tests := []struct {
		ref string
		id  string
		err string
	}{
		{ref: "m1.large", id: "flavor-large"},
		{ref: "flavor-twin-2", id: "flavor-twin-2"},
		{ref: "twin", err: `flavor "twin": 2 share that name, use an ID`},
		{ref: "m1.missing", err: `flavor "m1.missing": not found`},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			id, err := client.FindFlavor(t.Context(), tt.ref)

			if tt.err != "" {
				require.EqualError(t, err, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.id, id)
		})
	}
}

func TestFindNetwork(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /network/networks", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"networks": [
			{"id": "network-public", "name": "public"},
			{"id": "network-ci", "name": "ci"}
		]}`)
	})
	client := newTestClient(t, mux)

	network, err := client.FindNetwork(t.Context(), "public")
	require.NoError(t, err)
	assert.Equal(t, Network{ID: "network-public", Name: "public"}, network)

	network, err = client.FindNetwork(t.Context(), "network-ci")
	require.NoError(t, err)
	assert.Equal(t, Network{ID: "network-ci", Name: "ci"}, network)

	_, err = client.FindNetwork(t.Context(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFindImage(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /image/images", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal(t, "active", query.Get("status"))
		assert.Equal(t, "created_at:desc", query.Get("sort"))

		if query.Get("name") != "runner" {
			respond(t, w, http.StatusOK, `{"images": []}`)

			return
		}

		respond(t, w, http.StatusOK, `{"images": [
			{"id": "image-new", "name": "runner", "status": "active", "os_type": "linux", "architecture": "aarch64"},
			{"id": "image-old", "name": "runner", "status": "active"}
		]}`)
	})
	mux.HandleFunc("GET /image/images/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "image-ubuntu" {
			respond(t, w, http.StatusNotFound, `{}`)

			return
		}

		respond(t, w, http.StatusOK, `{"id": "image-ubuntu", "name": "Ubuntu 24.04", "status": "active", "os_type": "linux"}`)
	})
	client := newTestClient(t, mux)

	tests := []struct {
		ref   string
		image Image
		err   error
	}{
		{ref: "runner", image: Image{ID: "image-new", OSType: "linux", Architecture: "aarch64"}},
		{ref: "image-ubuntu", image: Image{ID: "image-ubuntu", OSType: "linux"}},
		{ref: "Debian 99", err: ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.ref, func(t *testing.T) {
			image, err := client.FindImage(t.Context(), tt.ref)

			if tt.err != nil {
				require.ErrorIs(t, err, tt.err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.image, image)
		})
	}
}

func TestImageRefUnmarshal(t *testing.T) {
	tests := []struct {
		json string
		id   string
	}{
		{json: `{"id": "image-ubuntu", "links": []}`, id: "image-ubuntu"},
		{json: `""`, id: ""},
		{json: `null`, id: ""},
	}

	for _, tt := range tests {
		t.Run(tt.json, func(t *testing.T) {
			var ref imageRef

			require.NoError(t, json.Unmarshal([]byte(tt.json), &ref))
			assert.Equal(t, tt.id, ref.ID)
		})
	}
}

func TestFindSecurityGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /network/security-groups", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"security_groups": [
			{"id": "group-ssh", "name": "ssh-from-manager"},
			{"id": "group-default-1", "name": "default"},
			{"id": "group-default-2", "name": "default"}
		]}`)
	})
	client := newTestClient(t, mux)

	id, err := client.FindSecurityGroup(t.Context(), "ssh-from-manager")
	require.NoError(t, err)
	assert.Equal(t, "group-ssh", id)

	id, err = client.FindSecurityGroup(t.Context(), "group-default-2")
	require.NoError(t, err)
	assert.Equal(t, "group-default-2", id)

	_, err = client.FindSecurityGroup(t.Context(), "default")
	require.EqualError(t, err, `security group "default": 2 share that name, use an ID`)

	_, err = client.FindSecurityGroup(t.Context(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestFindServerGroup(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /compute/os-server-groups", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"server_groups": [
			{"id": "group-workers", "name": "workers", "policy": "soft-anti-affinity"},
			{"id": "group-builders", "name": "builders", "policy": "anti-affinity"}
		]}`)
	})
	client := newTestClient(t, mux)

	id, err := client.FindServerGroup(t.Context(), "workers")
	require.NoError(t, err)
	assert.Equal(t, "group-workers", id)

	id, err = client.FindServerGroup(t.Context(), "group-builders")
	require.NoError(t, err)
	assert.Equal(t, "group-builders", id)

	_, err = client.FindServerGroup(t.Context(), "missing")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCheckKeyPair(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /compute/os-keypairs/{name}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("name") != "runner" {
			respond(t, w, http.StatusNotFound, `{"itemNotFound": {"code": 404}}`)

			return
		}

		respond(t, w, http.StatusOK, `{"keypair": {"name": "runner", "public_key": "ssh-ed25519 AAAA"}}`)
	})
	client := newTestClient(t, mux)

	require.NoError(t, client.CheckKeyPair(t.Context(), "runner"))
	require.ErrorIs(t, client.CheckKeyPair(t.Context(), "runer"), ErrNotFound)
}

// serveCloud serves mux behind a Keystone listing its compute, image and
// network endpoints, and returns the path of a clouds.yaml naming it "test".
func serveCloud(t *testing.T, mux *http.ServeMux) string {
	t.Helper()

	for _, name := range []string{"OS_REGION_NAME", "OS_INTERFACE", "OS_CACERT", "OS_CERT", "OS_KEY"} {
		t.Setenv(name, "")
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("POST /identity/v3/auth/tokens", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Subject-Token", "token")
		respond(t, w, http.StatusCreated, fmt.Sprintf(`{"token": {
			"expires_at": "2099-01-01T00:00:00Z",
			"catalog": [
				{"type": "compute", "endpoints": [{"interface": "public", "region_id": "RegionOne", "url": "%[1]s/compute/v2.1/"}]},
				{"type": "image", "endpoints": [{"interface": "public", "region_id": "RegionOne", "url": "%[1]s/image/"}]},
				{"type": "network", "endpoints": [{"interface": "public", "region_id": "RegionOne", "url": "%[1]s/network/"}]}
			]
		}}`, server.URL))
	})
	mux.HandleFunc("GET /image/{$}", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"versions": [{"id": "v2.16", "status": "CURRENT"}]}`)
	})
	mux.HandleFunc("GET /network/{$}", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"versions": [{"id": "v2.0", "status": "CURRENT"}]}`)
	})

	path := filepath.Join(t.TempDir(), "clouds.yaml")
	require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, `clouds:
  test:
    auth:
      auth_url: %s/identity/v3
      username: runner
      password: secret
      project_name: ci
      user_domain_name: Default
      project_domain_name: Default
    region_name: RegionOne
`, server.URL), 0o600))

	return path
}

// serveComputeVersion serves Nova's version document, with its newest
// microversion.
func serveComputeVersion(t *testing.T, mux *http.ServeMux, newest string) {
	t.Helper()

	mux.HandleFunc("GET /compute/v2.1/{$}", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, fmt.Sprintf(
			`{"version": {"id": "v2.1", "status": "CURRENT", "version": %q, "min_version": "2.1"}}`, newest))
	})
}

func TestNew(t *testing.T) {
	mux := http.NewServeMux()
	cloudsConfig := serveCloud(t, mux)
	serveComputeVersion(t, mux, "2.96")
	mux.HandleFunc("GET /compute/v2.1/servers/detail", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, microversion, r.Header.Get("X-OpenStack-Nova-API-Version"),
			"tags at creation need the microversion")
		respond(t, w, http.StatusOK, `{"servers": []}`)
	})
	mux.HandleFunc("GET /network/v2.0/networks", func(w http.ResponseWriter, _ *http.Request) {
		respond(t, w, http.StatusOK, `{"networks": [{"id": "network-public", "name": "public"}]}`)
	})

	client, err := New(t.Context(), Options{Cloud: "test", CloudsConfig: cloudsConfig})
	require.NoError(t, err)

	assert.Equal(t, "RegionOne", client.Region())

	_, err = client.ListServers(t.Context(), "fleeting:runner")
	require.NoError(t, err)

	network, err := client.FindNetwork(t.Context(), "public")
	require.NoError(t, err)
	assert.Equal(t, Network{ID: "network-public", Name: "public"}, network)
}

func TestNewFailsWithoutTheMicroversion(t *testing.T) {
	mux := http.NewServeMux()
	cloudsConfig := serveCloud(t, mux)
	serveComputeVersion(t, mux, "2.38")

	_, err := New(t.Context(), Options{Cloud: "test", CloudsConfig: cloudsConfig})

	require.ErrorContains(t, err, "negotiate the compute API version: microversion 2.52 not supported")
}
