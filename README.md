# MaxIOFS Desktop Agent

Desktop agent for mounting MaxIOFS buckets as local drives on Windows.

## Features

- System tray icon with interactive menu
- Mount S3/MaxIOFS buckets as Windows drive letters
- Read and write support for files and directories
- Intelligent metadata caching for better performance
- Visual configuration with native dialogs
- Multiple bucket management
- Auto-connect on startup
- Secure credential storage

## Requirements

### Windows
- **WinFsp** installed (https://github.com/winfsp/winfsp/releases)
  - Download and install `winfsp-x.x.xxxxx.msi`
- Windows 10 or later recommended

## Installation

1. Download the latest release
2. Install WinFsp (if not already installed)
3. Run `maxiofs-agent.exe`

## Usage

### Initial Setup

1. **Run the agent**
   ```bash
   maxiofs-agent.exe
   ```

2. **A system tray icon will appear** (look near the clock)

3. **Configure connection**
   - Right-click the tray icon
   - Select "Configure Connection"
   - Enter:
     - Endpoint (e.g., `localhost:8080` or `s3.example.com`)
     - Access Key ID
     - Secret Access Key
     - Use SSL/TLS (Yes/No)

### Mounting Buckets

1. Click the tray icon
2. Go to "Buckets" → Select a bucket
3. Enter a drive letter (e.g., `Z`)
4. The bucket will appear as a drive in Windows Explorer!

### Unmounting

- Click on the mounted bucket in the menu to unmount it

## Configuration

Configuration is automatically saved in:
- **Windows**: `C:\Users\<username>\.maxiofs-agent\config.json`

## Technical Details

- **Backend**: Go + AWS SDK for Go v2
- **Virtual Filesystem**: cgofuse + WinFsp
- **Interface**: systray + native dialogs
- **No web dependencies**: Pure native executable
- **Size**: ~13MB

## Supported Operations

- **Files**: Read, Write, Create, Delete, Rename
- **Directories**: Create, Delete, List, Rename
- **Metadata**: File size, modification time, permissions
- **Performance**: Intelligent caching for metadata and listings

## Building from Source

### Prerequisites

- Go 1.24 or later
- WinFsp SDK (for development)
- GCC (w64devkit or MinGW-w64)

### Build Commands

```bash
# Install dependencies
go mod download

# Build
go build -ldflags="-H windowsgui" -o maxiofs-agent.exe ./cmd/maxiofs-agent
```

Or use the provided build script:

```bash
build.bat
```

## Project Structure

```
maxiofs-agent/
├── cmd/
│   └── maxiofs-agent/     # Main application
├── internal/
│   ├── config/            # Configuration management
│   ├── storage/           # S3 client implementation
│   ├── vfs/               # Virtual filesystem (S3FS)
│   └── cgofuse/           # FUSE wrapper (vendored)
├── include/               # WinFsp headers
├── lib/                   # WinFsp libraries
└── README.md
```

## How It Works

1. The agent creates a system tray icon for user interaction
2. When you mount a bucket, it creates a virtual filesystem using WinFsp
3. Files are loaded on-demand (streaming from S3)
4. Write operations use temporary files that are uploaded on close/flush
5. Metadata is cached to reduce S3 API calls
6. The entire bucket is NOT downloaded - only requested files

## Troubleshooting

### "Could not mount bucket"
- Verify WinFsp is installed
- Try a different drive letter
- Check if the drive letter is already in use

### "Connection error"
- Verify endpoint is correct (no `http://` or `https://` prefix)
- Check access key and secret key
- Verify SSL/TLS setting matches your server

### Files don't appear
- Wait a few seconds for the cache to refresh
- Try unmounting and remounting the bucket
- Check S3 server logs for errors

## Future Features

- [ ] Intelligent local cache for files
- [ ] Bidirectional sync
- [ ] Change notifications
- [ ] Usage statistics
- [ ] Offline mode
- [ ] Linux and macOS support

## License

MIT
