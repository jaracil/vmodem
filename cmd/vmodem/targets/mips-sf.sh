#! /bin/bash
CC=mips-linux-muslsf-gcc CGO_ENABLED=1 GOOS=linux GOARCH=mips GOMIPS=softfloat go build -o vmodem_mips-sf -ldflags="-s -w -extldflags=-static"