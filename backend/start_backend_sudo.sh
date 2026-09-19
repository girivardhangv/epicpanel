#!/bin/bash

echo "=========================================================="
echo "    Starting EpicPanel Backend with sudo (Root Access)    "
echo "=========================================================="
echo ""
echo "The backend needs root access to install software using apt-get,"
echo "create directories in /opt, and set up system configurations."
echo ""
echo "Please enter your sudo password when prompted."
echo ""

sudo EPICPANEL_DATABASE_URL="postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_dev?sslmode=disable" /home/giri/.local/go-bin/bin/go run ./cmd/api
