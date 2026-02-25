#!/bin/bash

if [ ! -d ~/.aws ] ;
  then
    mkdir  ~/.aws/ ;
fi

cat > ~/.aws/credentials <<-EOF
[default]
aws_access_key_id = ${AWS_ACCESS_KEY_ID}
aws_secret_access_key = ${AWS_SECRET_ACCESS_KEY}
EOF

goofys videos-svc-bucket-staging:public/livestream /tmp/livestream &

./app --config /root/config.yaml