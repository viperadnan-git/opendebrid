# /bin/bash

# Re-format staged files only

INCLUDE_EXTENSIONS="go"

git diff --name-only --cached | while read -r file; do
  if [ -f "$file" ]; then
    # Get file extension
    extension="${file##*.}"
    
    # Check if extension exactly matches one in INCLUDE_EXTENSIONS
    if echo "$INCLUDE_EXTENSIONS" | tr ',' '\n' | grep -Fx "$extension" > /dev/null; then
      echo "Formatting $file"
      go fmt "$file" 
      go run golang.org/x/tools/cmd/goimports@latest -w "$file"
    fi
  fi
done

# update staged files
git update-index --again

# Run linting
make lint