// Copyright (c) 2014-2017 The btcsuite developers
// Use of this source code is governed by an ISC
// license that can be found in the LICENSE file.

/*
Package rpcclient implements a Bitcoin Core JSON-RPC client.

Overview

This client provides a robust and easy to use client for interfacing with a
Bitcoin RPC server that uses a Bitcoin Core compatible Bitcoin JSON-RPC API.


Errors

There are 3 categories of errors that will be returned throughout this package:

  - Errors related to the client connection such as authentication and shutdown
  - Errors that occur before communicating with the remote RPC server such as
    command creation and marshaling errors or issues talking to the remote
    server
  - Errors returned from the remote RPC server like unimplemented commands,
    nonexistent requested blocks and transactions, malformed data, and incorrect
    networks

The first category of errors are typically ErrInvalidAuth or ErrClientShutdown.

The second category of errors typically indicates a programmer error and as such
the type can vary, but usually will be best handled by simply showing/logging
it.

The third category of errors, that is errors returned by the server, can be
detected by type asserting the error in a *btcjson.RPCError.  For example, to
detect if a command is unimplemented by the remote RPC server:

  amount, err := client.GetBalance("")
  if err != nil {
  	if jerr, ok := err.(*btcjson.RPCError); ok {
  		switch jerr.Code {
  		case btcjson.ErrRPCUnimplemented:
  			// Handle not implemented error

  		// Handle other specific errors you care about
		}
  	}

  	// Log or otherwise handle the error knowing it was not one returned
  	// from the remote RPC server.
  }

Example Usage

Check the examples directory.
*/
package rpcclient
