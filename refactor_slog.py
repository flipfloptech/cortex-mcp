import os
import re

def replace_in_file(filepath):
    with open(filepath, 'r') as f:
        content = f.read()

    # Replace imports
    content = re.sub(r'"log/slog"', '"go.uber.org/zap"', content)

    # Replace calls
    content = re.sub(r'slog\.Info\(', 'zap.S().Infow(', content)
    content = re.sub(r'slog\.Error\(', 'zap.S().Errorw(', content)
    content = re.sub(r'slog\.Debug\(', 'zap.S().Debugw(', content)
    content = re.sub(r'slog\.Warn\(', 'zap.S().Warnw(', content)

    with open(filepath, 'w') as f:
        f.write(content)

files_to_update = [
    'internal/mesh/cli.go',
    'internal/mesh/lifecycle.go',
    'internal/mesh/upgrade.go',
    'internal/registry/registry.go'
]

for f in files_to_update:
    replace_in_file(f)

