#!/bin/bash

GOPATH=/home/ecatala/Codi/gopath
docker run --rm -v "$GOPATH":/go -v "$PWD":/out golang:1.9-alpine3.7 go build -v -i -o /out/auto-lb src/github.com/indiketa/docker-loadbalancer/main.go
docker build . -t indiketa/docker-loadbalancer:1.0-haproxy1.8-alpine3.7
