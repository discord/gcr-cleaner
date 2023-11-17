#!/bin/bash
docker build -t gcr-cleaner-test .

docker run gcr-cleaner-test make test
