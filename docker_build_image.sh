#!/bin/sh

docker rm --force $IMAGE_NAME:$IMAGE_TAG
docker image rm $IMAGE_NAME:$IMAGE_TAG
docker build \
  --platform linux/amd64 \
  --build-arg GITHUB_TOKEN=$GITHUB_TOKEN \
  -t $IMAGE_NAME:$IMAGE_TAG .

