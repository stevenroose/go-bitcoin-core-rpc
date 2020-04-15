// Copyright (c) 2014-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

package rpcclient

import (
	"bytes"
	"container/list"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/KiteShi/go-bitcoin-core-rpc/btcjson"
)

var (
	// ErrClientShutdown is an error to describe the condition where the
	// client is either already shutdown, or in the process of shutting
	// down.  Any outstanding futures when a client shutdown occurs will
	// return this error as will any new requests.
	ErrClientShutdown = errors.New("the client has been shutdown")
)

const (
	// sendPostBufferSize is the number of elements the HTTP POST send
	// channel can queue before blocking.
	sendPostBufferSize = 100
)

// sendPostDetails houses an HTTP POST request to send to an RPC server as well
// as the original JSON-RPC command and a channel to reply on when the server
// responds with the result.
type sendPostDetails struct {
	httpRequest *http.Request
	jsonRequest *jsonRequest
}

// jsonRequest holds information about a json request that is used to properly
// detect, interpret, and deliver a reply to it.
type jsonRequest struct {
	id             uint64
	method         string
	cmd            interface{}
	marshalledJSON []byte
	responseChan   chan *response
}

// Client represents a Bitcoin RPC client which allows easy access to the
// various RPC methods available on a Bitcoin RPC server.  Each of the wrapper
// functions handle the details of converting the passed and return types to and
// from the underlying JSON types which are required for the JSON-RPC
// invocations
//
// The client provides each RPC in both synchronous (blocking) and asynchronous
// (non-blocking) forms.  The asynchronous forms are based on the concept of
// futures where they return an instance of a type that promises to deliver the
// result of the invocation at some future time.  Invoking the Receive method on
// the returned future will block until the result is available if it's not
// already.
type Client struct {
	id uint64 // atomic, so must stay 64-bit aligned

	// config holds the connection configuration assoiated with this client.
	config *ConnConfig

	// httpClient is the underlying HTTP client to use when running in HTTP
	// POST mode.
	httpClient *http.Client

	// Track command and their response channels by ID.
	requestLock sync.Mutex
	requestList *list.List

	// Networking infrastructure.
	sendPostChan chan *sendPostDetails
	shutdown     chan struct{}
	wg           sync.WaitGroup
}

// NextID returns the next id to be used when sending a JSON-RPC message.  This
// ID allows responses to be associated with particular requests per the
// JSON-RPC specification.  Typically the consumer of the client does not need
// to call this function, however, if a custom request is being created and used
// this function should be used to ensure the ID is unique amongst all requests
// being made.
func (c *Client) NextID() uint64 {
	return atomic.AddUint64(&c.id, 1)
}

// rawResponse is a partially-unmarshaled JSON-RPC response.  For this
// to be valid (according to JSON-RPC 1.0 spec), ID may not be nil.
type rawResponse struct {
	ID     *float64          `json:"id"`
	Result json.RawMessage   `json:"result"`
	Error  *btcjson.RPCError `json:"error"`
}

// response is the raw bytes of a JSON-RPC result, or the error if the response
// error object was non-null.
type response struct {
	result []byte
	err    error
}

// result checks whether the unmarshaled response contains a non-nil error,
// returning an unmarshaled btcjson.RPCError (or an unmarshaling error) if so.
// If the response is not an error, the raw bytes of the request are
// returned for further unmashaling into specific result types.
func (r rawResponse) result() (result []byte, err error) {
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Result, nil
}

// handleSendPostMessage handles performing the passed HTTP request, reading the
// result, unmarshalling it, and delivering the unmarshalled result to the
// provided response channel.
func (c *Client) handleSendPostMessage(details *sendPostDetails) {
	jReq := details.jsonRequest
	log.Tracef("Sending command [%s] with id %d", jReq.method, jReq.id)
	httpResponse, err := c.httpClient.Do(details.httpRequest)
	if err != nil {
		jReq.responseChan <- &response{err: err}
		return
	}

	// Read the raw bytes and close the response.
	respBytes, err := ioutil.ReadAll(httpResponse.Body)
	_ = httpResponse.Body.Close()
	if err != nil {
		err = fmt.Errorf("error reading json reply: %v", err)
		jReq.responseChan <- &response{err: err}
		return
	}

	// Try to unmarshal the response as a regular JSON-RPC response.
	var resp rawResponse
	err = json.Unmarshal(respBytes, &resp)
	if err != nil {
		// When the response itself isn't a valid JSON-RPC response
		// return an error which includes the HTTP status code and raw
		// response bytes.
		err = fmt.Errorf("status code: %d, response: %q",
			httpResponse.StatusCode, string(respBytes))
		jReq.responseChan <- &response{err: err}
		return
	}

	res, err := resp.result()
	jReq.responseChan <- &response{result: res, err: err}
}

// sendPostHandler handles all outgoing messages when the client is running
// in HTTP POST mode.  It uses a buffered channel to serialize output messages
// while allowing the sender to continue running asynchronously.  It must be run
// as a goroutine.
func (c *Client) sendPostHandler() {
out:
	for {
		// Send any messages ready for send until the shutdown channel
		// is closed.
		select {
		case details := <-c.sendPostChan:
			c.handleSendPostMessage(details)

		case <-c.shutdown:
			break out
		}
	}

	// Drain any wait channels before exiting so nothing is left waiting
	// around to send.
cleanup:
	for {
		select {
		case details := <-c.sendPostChan:
			details.jsonRequest.responseChan <- &response{
				result: nil,
				err:    ErrClientShutdown,
			}

		default:
			break cleanup
		}
	}
	c.wg.Done()
	log.Tracef("RPC client send handler done for %s", c.config.Host)

}

// sendPostRequest sends the passed HTTP request to the RPC server using the
// HTTP client associated with the client.  It is backed by a buffered channel,
// so it will not block until the send channel is full.
func (c *Client) sendPostRequest(httpReq *http.Request, jReq *jsonRequest) {
	// Don't send the message if shutting down.
	select {
	case <-c.shutdown:
		jReq.responseChan <- &response{result: nil, err: ErrClientShutdown}
	default:
	}

	c.sendPostChan <- &sendPostDetails{
		jsonRequest: jReq,
		httpRequest: httpReq,
	}
}

// newFutureError returns a new future result channel that already has the
// passed error waitin on the channel with the reply set to nil.  This is useful
// to easily return errors from the various Async functions.
func newFutureError(err error) chan *response {
	responseChan := make(chan *response, 1)
	responseChan <- &response{err: err}
	return responseChan
}

// receiveFuture receives from the passed futureResult channel to extract a
// reply or any errors.  The examined errors include an error in the
// futureResult and the error in the reply from the server.  This will block
// until the result is available on the passed channel.
func receiveFuture(f chan *response) ([]byte, error) {
	// Wait for a response on the returned channel.
	r := <-f
	return r.result, r.err
}

// sendRequest sends the passed request to the server by issuing an HTTP POST
// request using the provided response channel for the reply.  Typically a new
// connection is opened and closed for each command when using this method,
// however, the underlying HTTP client might coalesce multiple commands
// depending on several factors including the remote server configuration.
func (c *Client) sendRequest(jReq *jsonRequest) {
	// Generate a request to the configured RPC server.
	protocol := "http"
	if c.config.EnableTLS {
		protocol = "https"
	}
	url := protocol + "://" + c.config.Host
	bodyReader := bytes.NewReader(jReq.marshalledJSON)
	httpReq, err := http.NewRequest("POST", url, bodyReader)
	if err != nil {
		jReq.responseChan <- &response{result: nil, err: err}
		return
	}
	httpReq.Close = true
	httpReq.Header.Set("Content-Type", "application/json")

	// Configure basic access authorization.
	httpReq.SetBasicAuth(c.config.User, c.config.Pass)

	log.Tracef("Sending command [%s] with id %d", jReq.method, jReq.id)
	c.sendPostRequest(httpReq, jReq)
}

// sendCmd sends the passed command to the associated server and returns a
// response channel on which the reply will be delivered at some point in the
// future.
func (c *Client) sendCmd(cmd interface{}) chan *response {
	// Get the method associated with the command.
	method, err := btcjson.CmdMethod(cmd)
	if err != nil {
		return newFutureError(err)
	}

	// Marshal the command.
	id := c.NextID()
	marshalledJSON, err := btcjson.MarshalCmd(id, cmd)
	if err != nil {
		return newFutureError(err)
	}

	// Generate the request and send it along with a channel to respond on.
	responseChan := make(chan *response, 1)
	jReq := &jsonRequest{
		id:             id,
		method:         method,
		cmd:            cmd,
		marshalledJSON: marshalledJSON,
		responseChan:   responseChan,
	}
	c.sendRequest(jReq)

	return responseChan
}

// sendCmdAndWait sends the passed command to the associated server, waits
// for the reply, and returns the result from it.  It will return the error
// field in the reply if there is one.
func (c *Client) sendCmdAndWait(cmd interface{}) (interface{}, error) {
	// Marshal the command to JSON-RPC, send it to the connected server, and
	// wait for a response on the returned channel.
	return receiveFuture(c.sendCmd(cmd))
}

// doShutdown closes the shutdown channel and logs the shutdown unless shutdown
// is already in progress.  It will return false if the shutdown is not needed.
//
// This function is safe for concurrent access.
func (c *Client) doShutdown() bool {
	// Ignore the shutdown request if the client is already in the process
	// of shutting down or already shutdown.
	select {
	case <-c.shutdown:
		return false
	default:
	}

	log.Tracef("Shutting down RPC client %s", c.config.Host)
	close(c.shutdown)
	return true
}

// Shutdown shuts down the client.
func (c *Client) Shutdown() {
	// Do the shutdown under the request lock to prevent clients from
	// adding new requests while the client shutdown process is initiated.
	c.requestLock.Lock()
	defer c.requestLock.Unlock()

	// Ignore the shutdown request if the client is already in the process
	// of shutting down or already shutdown.
	if !c.doShutdown() {
		return
	}

	// Send the ErrClientShutdown error to any pending requests.
	for e := c.requestList.Front(); e != nil; e = e.Next() {
		req := e.Value.(*jsonRequest)
		req.responseChan <- &response{
			result: nil,
			err:    ErrClientShutdown,
		}
	}
	c.requestList.Init()
}

// start begins processing input and output messages.
func (c *Client) start() {
	log.Tracef("Starting RPC client %s", c.config.Host)

	c.wg.Add(1)
	go c.sendPostHandler()
}

// WaitForShutdown blocks until the client goroutines are stopped and the
// connection is closed.
func (c *Client) WaitForShutdown() {
	c.wg.Wait()
}

// ConnConfig describes the connection configuration parameters for the client.
// This
type ConnConfig struct {
	// Host is the IP address and port of the RPC server you want to connect
	// to.
	Host string

	// User is the username to use to authenticate to the RPC server.
	User string

	// Pass is the passphrase to use to authenticate to the RPC server.
	Pass string

	// EnableTLS specifies whether transport layer security should be
	// enabled.  It is recommended to always use TLS if the RPC server
	// supports it as otherwise your username and password is sent across
	// the wire in cleartext.
	EnableTLS bool

	// Certificates are the bytes for a PEM-encoded certificate chain used
	// for the TLS connection.  It has no effect if the EnableTLS parameter
	// is false.
	Certificates []byte

	// Proxy specifies to connect through a SOCKS 5 proxy server.  It may
	// be an empty string if a proxy is not required.
	Proxy string

	// ProxyUser is an optional username to use for the proxy server if it
	// requires authentication.  It has no effect if the Proxy parameter
	// is not set.
	ProxyUser string

	// ProxyPass is an optional password to use for the proxy server if it
	// requires authentication.  It has no effect if the Proxy parameter
	// is not set.
	ProxyPass string

	// EnableBCInfoHacks is an option provided to enable compatibility hacks
	// when connecting to blockchain.info RPC server
	EnableBCInfoHacks bool
}

// newHTTPClient returns a new http client that is configured according to the
// proxy and TLS settings in the associated connection configuration.
func newHTTPClient(config *ConnConfig) (*http.Client, error) {
	// Set proxy function if there is a proxy configured.
	var proxyFunc func(*http.Request) (*url.URL, error)
	if config.Proxy != "" {
		proxyURL, err := url.Parse(config.Proxy)
		if err != nil {
			return nil, err
		}
		proxyFunc = http.ProxyURL(proxyURL)
	}

	// Configure TLS if needed.
	var tlsConfig *tls.Config
	if config.EnableTLS {
		if len(config.Certificates) > 0 {
			pool := x509.NewCertPool()
			pool.AppendCertsFromPEM(config.Certificates)
			tlsConfig = &tls.Config{
				RootCAs: pool,
			}
		}
	}

	client := http.Client{
		Transport: &http.Transport{
			Proxy:           proxyFunc,
			TLSClientConfig: tlsConfig,
		},
	}

	return &client, nil
}

// New creates a new RPC client based on the provided connection configuration
// details.  The notification handlers parameter may be nil if you are not
// interested in receiving notifications and will be ignored if the
// configuration is set to run in HTTP POST mode.
func New(config *ConnConfig) (*Client, error) {
	httpClient, err := newHTTPClient(config)
	if err != nil {
		return nil, err
	}

	client := &Client{
		config:       config,
		httpClient:   httpClient,
		requestList:  list.New(),
		sendPostChan: make(chan *sendPostDetails, sendPostBufferSize),
		shutdown:     make(chan struct{}),
	}

	log.Infof("Established connection to RPC server %s", config.Host)
	client.start()

	return client, nil
}
