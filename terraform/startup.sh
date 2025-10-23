#!/bin/bash
set -e

# Update system
apt update -y
apt install -y git golang-go redis-server postgresql postgresql-contrib openssl

# Enable Redis & Postgres
systemctl enable redis-server
systemctl enable postgresql
systemctl start redis-server
systemctl start postgresql

# Setup Postgres user & DB
# If DB_PASSWORD is provided via environment, use it. Otherwise generate a strong random password and store it on the VM.
if sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='urlshort'" | grep -q 1; then
  echo "Postgres user 'urlshort' already exists, skipping creation"
else
  if [ -z "$DB_PASSWORD" ]; then
    echo "DB_PASSWORD not provided — generating a strong random password and saving to /root/urlshorter_creds.txt"
    DB_PASSWORD=$(openssl rand -base64 16)
    echo "DB_PASSWORD=$DB_PASSWORD" > /root/urlshorter_creds.txt
    chmod 600 /root/urlshorter_creds.txt
  fi
  sudo -u postgres psql -c "CREATE USER urlshort WITH PASSWORD '$DB_PASSWORD';"
  sudo -u postgres psql -c "CREATE DATABASE urlshort OWNER urlshort;"
fi

# Clone your repo
cd /home/azureuser
if [ ! -d "urlshorter" ]; then
  git clone https://github.com/sodiqscript111/urlshortener.git
fi
cd urlshorter || exit 0

# Build & run app
go build -o app .
nohup ./app > app.log 2>&1 &
