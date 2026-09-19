#!/bin/bash

echo "=========================================================="
echo "    Updating EpicPanel Agent                              "
echo "=========================================================="
echo ""
echo "Moving newly compiled agent to /usr/local/bin..."
sudo mv /home/giri/projects/EpicPanel/backend/epicpanel-agent /usr/local/bin/epicpanel-agent
sudo chmod +x /usr/local/bin/epicpanel-agent

echo "Restarting the epicpanel-agent service..."
sudo systemctl restart epicpanel-agent

echo "Agent updated successfully!"
