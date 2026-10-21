#!/bin/bash

# SmartTodo Runner
# Simple script to run SmartTodo with proper environment

# Check if .env exists
if [ ! -f "../../.env" ]; then
    echo "❌ Error: .env file not found"
    echo "Please ensure ../../.env contains:"
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