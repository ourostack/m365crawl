#!/bin/sh
# ax-xray synthetic WorkIQ stub: made-up data only. It records each call beside itself and answers a fixed synthetic
# reply, so an X-ray run never reaches a real service. The X-ray's --guard (assert-stub.sh) proves this file is the stub.
here="$(dirname "$0")"
echo "$*" >> "$here/calls.log"
echo '{"synthetic":true,"status":"stub"}'
