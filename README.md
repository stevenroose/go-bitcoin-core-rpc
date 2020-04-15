go-bitcoin-core-rpc
===================

[![ISC License](http://img.shields.io/badge/license-ISC-blue.svg)](http://copyfree.org)
[![GoDoc](https://img.shields.io/badge/godoc-reference-blue.svg)](http://godoc.org/github.com/KiteShi/go-bitcoin-core-rpc)

rpcclient implements a Bitcoin Core JSON-RPC client package written
in [Go](http://golang.org/).  It provides a robust and easy to use client for
interfacing with a Bitcoin RPC server.

## Status

This package is currently under active development.  It is already stable and
the infrastructure is complete.  However, there are still several RPCs left to
implement and the API is not stable yet.

## Documentation

* [API Reference](http://godoc.org/github.com/KiteShi/go-bitcoin-core-rpc)
* [Example](https://github.com/KiteShi/go-bitcoin-core-rpc/tree/master/examples)
  Connects to a bitcoin Core RPC server using HTTP POST mode with TLS disabled
  and gets the current block count.

## Major Features

* Translates to and from higher-level and easier to use Go types
* Offers a synchronous (blocking) and asynchronous API

## Installation

```bash
$ go get -u github.com/KiteShi/go-bitcoin-core-rpc
```

## License

Package rpcclient is licensed under the [copyfree](http://copyfree.org) ISC
License.
