#!/bin/sh
# Proves the file named by $1 is the ax-xray synthetic WorkIQ stub, by its marker line. Used as the X-ray's --guard.
grep -q "ax-xray synthetic WorkIQ stub" "$1"
