// Package cloud wraps the OpenStack calls the plugin makes.
package cloud

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound reports a missing resource.
var ErrNotFound = errors.New("not found")

// Client is the set of OpenStack calls the plugin makes. Lookups by
// reference accept a name or an ID.
type Client interface {
	Region() string
	ListServers(ctx context.Context, tag string) ([]Server, error)
	GetServer(ctx context.Context, id string) (Server, error)
	CreateServer(ctx context.Context, req CreateRequest) (id string, err error)
	DeleteServer(ctx context.Context, id string) error // succeeds when already gone
	FindFlavor(ctx context.Context, ref string) (id string, err error)
	FindNetwork(ctx context.Context, ref string) (Network, error)
	FindSecurityGroup(ctx context.Context, ref string) (id string, err error)
	FindServerGroup(ctx context.Context, ref string) (id string, err error)
	CheckKeyPair(ctx context.Context, name string) error
	// FindImage returns the newest active image with that name, or else the
	// image with that ID.
	FindImage(ctx context.Context, ref string) (Image, error)
	GetImage(ctx context.Context, id string) (Image, error)
}

// Server is a Nova server.
type Server struct {
	ID        string
	Name      string
	Status    string // BUILD, ACTIVE, ERROR...
	TaskState string // operation in progress, such as deleting
	Created   time.Time
	ImageID   string // empty for a volume-backed server
	Fault     string // Nova's reason for an ERROR
	// Addresses are sorted by network name, in Nova's order within a network.
	Addresses []Address
}

// Address is an IP address of a server.
type Address struct {
	Network  string // network name
	IP       string
	Version  int // 4 or 6
	Floating bool
}

// Network is a Neutron network.
type Network struct {
	ID   string
	Name string // the key of a server's addresses on it
}

// Image is a Glance image.
type Image struct {
	ID           string
	OSType       string // os_type property: linux, windows...
	Architecture string // architecture property: x86_64, aarch64...
}

// CreateRequest is a server to create.
type CreateRequest struct {
	Name             string
	FlavorID         string
	ImageID          string
	NetworkIDs       []string
	SecurityGroupIDs []string
	KeyName          string
	UserData         string
	ConfigDrive      bool
	Metadata         map[string]string
	Tags             []string
	AvailabilityZone string
	ServerGroupID    string
}
