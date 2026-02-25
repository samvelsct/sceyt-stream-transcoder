#!/bin/bash

docker run -d \
  --cap-add SYS_ADMIN \
  --device /dev/fuse \
  --security-opt apparmor:unconfined \
  -e AWS_ACCESS_KEY_ID=YOUR_AWS_ACCESS_KEY_ID \
  -e AWS_SECRET_ACCESS_KEY=YOUR_AWS_SECRET_ACCESS_KEY \
  -p 50067:50065 --name vt-stream-transcoder vt-stream-transcoder:latest