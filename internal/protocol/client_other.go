//go:build !windows

package protocol

import (
	"errors"
	"time"
)

// Client is a compile-time placeholder for non-Windows development hosts.
type Client struct {
	PipeName string
	Timeout  time.Duration
}

func NewClient() *Client { return &Client{PipeName: PipeName, Timeout: DefaultTimeout} }

var errUnsupported = errors.New("SkyBootstrap is only available on Windows")

func (c *Client) Load(string) error                 { return errUnsupported }
func (c *Client) Unload(string) error               { return errUnsupported }
func (c *Client) Ping() error                       { return errUnsupported }
func (c *Client) UI(string, []byte) ([]byte, error) { return nil, errUnsupported }
func (c *Client) Poll(string) ([][]byte, error)     { return nil, errUnsupported }
func (c *Client) List() ([]string, error)           { return nil, errUnsupported }
