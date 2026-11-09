// Package fake is an in-memory cloud.Client for tests.
package fake

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/radome-io/fleeting-plugin-openstack/internal/cloud"
)

var _ cloud.Client = (*Client)(nil)

var (
	errQuotaExceeded = errors.New("quota exceeded")
	errLocked        = errors.New("server is locked")
)

// Client is an in-memory cloud. Change its fields between calls only.
type Client struct {
	RegionName string

	// What the lookups find, by name.
	Flavors        map[string]string
	Networks       map[string]string
	SecurityGroups map[string]string
	Images         map[string]cloud.Image
	KeyPairs       map[string]bool

	// Returned by the matching calls when set.
	ListErr     error
	CreateErr   error
	DeleteErr   error
	GetImageErr error

	Quota  int             // most servers CreateServer creates, when set
	Locked map[string]bool // servers DeleteServer refuses to delete

	mu             sync.Mutex
	servers        map[string]entry
	created        []cloud.CreateRequest
	deleted        []string
	createAttempts int
	deleteAttempts map[string]int
	nextID         int
}

type entry struct {
	server cloud.Server
	tags   []string
}

// Add stores a server carrying the given tags.
func (c *Client) Add(server cloud.Server, tags ...string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.servers == nil {
		c.servers = make(map[string]entry)
	}
	c.servers[server.ID] = entry{server: server, tags: tags}
}

// Set replaces a stored server, keeping its tags.
func (c *Client) Set(server cloud.Server) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.servers[server.ID]
	if !ok {
		return fmt.Errorf("server %s: %w", server.ID, cloud.ErrNotFound)
	}
	e.server = server
	c.servers[server.ID] = e

	return nil
}

// CreateAttempts counts CreateServer calls.
func (c *Client) CreateAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.createAttempts
}

// DeleteAttempts counts DeleteServer calls for a server.
func (c *Client) DeleteAttempts(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.deleteAttempts[id]
}

// Created returns the successful create requests.
func (c *Client) Created() []cloud.CreateRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.created)
}

// Deleted returns the IDs of the servers deleted.
func (c *Client) Deleted() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return slices.Clone(c.deleted)
}

// Region returns RegionName.
func (c *Client) Region() string {
	return c.RegionName
}

// ListServers returns the servers carrying the tag, by ID.
func (c *Client) ListServers(_ context.Context, tag string) ([]cloud.Server, error) {
	if c.ListErr != nil {
		return nil, c.ListErr
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	var result []cloud.Server
	for _, e := range c.servers {
		if slices.Contains(e.tags, tag) {
			result = append(result, e.server)
		}
	}
	slices.SortFunc(result, func(a, b cloud.Server) int { return cmp.Compare(a.ID, b.ID) })

	return result, nil
}

// GetServer returns a server.
func (c *Client) GetServer(_ context.Context, id string) (cloud.Server, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.servers[id]
	if !ok {
		return cloud.Server{}, fmt.Errorf("server %s: %w", id, cloud.ErrNotFound)
	}

	return e.server, nil
}

// CreateServer adds a server in BUILD.
func (c *Client) CreateServer(_ context.Context, req cloud.CreateRequest) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.createAttempts++
	if c.CreateErr != nil {
		return "", c.CreateErr
	}
	if c.Quota > 0 && len(c.servers) >= c.Quota {
		return "", errQuotaExceeded
	}

	c.nextID++
	id := "server-" + strconv.Itoa(c.nextID)

	if c.servers == nil {
		c.servers = make(map[string]entry)
	}
	c.servers[id] = entry{
		server: cloud.Server{ID: id, Name: req.Name, Status: "BUILD", Created: time.Now(), ImageID: req.ImageID},
		tags:   slices.Clone(req.Tags),
	}
	c.created = append(c.created, req)

	return id, nil
}

// DeleteServer marks a server as being deleted, as Nova does until Reap.
func (c *Client) DeleteServer(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.deleteAttempts == nil {
		c.deleteAttempts = make(map[string]int)
	}
	c.deleteAttempts[id]++
	if c.DeleteErr != nil {
		return c.DeleteErr
	}
	if c.Locked[id] {
		return fmt.Errorf("delete server %s: %w", id, errLocked)
	}

	e, ok := c.servers[id]
	if !ok {
		return nil
	}
	if e.server.TaskState != "deleting" {
		e.server.TaskState = "deleting"
		c.servers[id] = e
		c.deleted = append(c.deleted, id)
	}

	return nil
}

// Reap removes the servers being deleted.
func (c *Client) Reap() {
	c.mu.Lock()
	defer c.mu.Unlock()

	maps.DeleteFunc(c.servers, func(_ string, e entry) bool { return e.server.TaskState == "deleting" })
}

// FindFlavor looks in Flavors.
func (c *Client) FindFlavor(_ context.Context, ref string) (string, error) {
	return find("flavor", c.Flavors, ref)
}

// FindNetwork looks in Networks.
func (c *Client) FindNetwork(_ context.Context, ref string) (string, error) {
	return find("network", c.Networks, ref)
}

// FindSecurityGroup looks in SecurityGroups.
func (c *Client) FindSecurityGroup(_ context.Context, ref string) (string, error) {
	return find("security group", c.SecurityGroups, ref)
}

// CheckKeyPair looks in KeyPairs.
func (c *Client) CheckKeyPair(_ context.Context, name string) error {
	if !c.KeyPairs[name] {
		return fmt.Errorf("key pair %q: %w", name, cloud.ErrNotFound)
	}

	return nil
}

// FindImage looks in Images.
func (c *Client) FindImage(ctx context.Context, ref string) (cloud.Image, error) {
	if image, ok := c.Images[ref]; ok {
		return image, nil
	}

	return c.GetImage(ctx, ref)
}

// GetImage looks in Images by ID.
func (c *Client) GetImage(_ context.Context, id string) (cloud.Image, error) {
	if c.GetImageErr != nil {
		return cloud.Image{}, c.GetImageErr
	}

	for _, image := range c.Images {
		if image.ID == id {
			return image, nil
		}
	}

	return cloud.Image{}, fmt.Errorf("image %q: %w", id, cloud.ErrNotFound)
}

func find(kind string, byName map[string]string, ref string) (string, error) {
	if id, ok := byName[ref]; ok {
		return id, nil
	}
	for _, id := range byName {
		if id == ref {
			return id, nil
		}
	}

	return "", fmt.Errorf("%s %q: %w", kind, ref, cloud.ErrNotFound)
}
