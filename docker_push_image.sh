#!/bin/sh

docker tag vt-stream-transcoder:latest dockerregistry.waafi.com/dockerman/vt-stream-transcoder:latest

docker push dockerregistry.waafi.com/dockerman/vt-stream-transcoder:latest

#docker tag vt-stream-transcoder:latest lhr.ocir.io/lrdw6bnwjjsi/vt-stream-transcoder:production-v2
#
#docker push lhr.ocir.io/lrdw6bnwjjsi/vt-stream-transcoder:production-v2
