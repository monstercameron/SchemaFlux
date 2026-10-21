#!/bin/bash

# SmartTodo Runner
# Simple script to run SmartTodo with proper environment

# Match the app's loadNearestEnv behavior: accept a .env in this directory or
# any parent directory, including the repository root after the v2 relocation.
env_dir=$PWD
env_file=
while :; do
    if [ -f "$env_dir/.env" ]; then
        env_file="$env_dir/.env"
        break
    fi
    parent_dir=${env_dir%/*}
    if [ -z "$parent_dir" ]; then
        parent_dir=/
    fi
    if [ "$parent_dir" = "$env_dir" ]; then
        break
    fi
    env_dir=$parent_dir
done

if [ -z "$env_file" ]; then
    echo "❌ Error: .env file not found in this directory or an ancestor"
    echo "Please create a .env file containing:"
    echo "  OPENAI_API_KEY=your-api-key"
    exit 1
fi

# Always rebuild to ensure latest changes are included
echo "🔨 Building SmartTodo..."
go build -o smarttodo ./cmd/smarttodo

# Check if build succeeded
if [ $? -ne 0 ]; then
    echo "❌ Build failed"
    exit 1
fi

echo "✅ Build successful"

# Run the app
echo "🚀 Starting SmartTodo..."
./smarttodo
