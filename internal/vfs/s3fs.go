package vfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"maxiofs-agent/internal/cgofuse"
	"maxiofs-agent/internal/storage"
)

// S3FS implementa el filesystem virtual para S3
type S3FS struct {
	cgofuse.FileSystemBase
	s3Client   *storage.S3Client
	bucketName string
	cache      *FileCache
	openFiles  map[uint64]*OpenFile
	nextFh     uint64

	// Cache para Statfs
	statfsCache     *cgofuse.Statfs_t
	statfsCacheTime time.Time
	statfsCacheTTL  time.Duration

	// Cache para ListObjects
	listCache     []storage.ObjectInfo
	listCacheTime time.Time
	listCacheTTL  time.Duration

	mu sync.RWMutex
}

// FileCache cachea metadata de archivos
type FileCache struct {
	entries map[string]*CacheEntry
	mu      sync.RWMutex
}

type CacheEntry struct {
	Info      storage.ObjectInfo
	ExpiresAt time.Time
}

// OpenFile representa un archivo abierto para escritura
type OpenFile struct {
	Path     string
	TempFile string // Archivo temporal en disco
	Size     int64
	Dirty    bool
}

// NewS3FS crea un nuevo filesystem S3
func NewS3FS(s3Client *storage.S3Client, bucketName string) *S3FS {
	return &S3FS{
		s3Client:   s3Client,
		bucketName: bucketName,
		cache: &FileCache{
			entries: make(map[string]*CacheEntry),
		},
		openFiles:      make(map[uint64]*OpenFile),
		nextFh:         1,
		statfsCacheTTL: 30 * time.Second, // Cachear por 30 segundos
		listCacheTTL:   2 * time.Second,  // Cache corto para listados
	}
}

// invalidateCaches invalida todos los caches cuando se modifica el filesystem
func (fs *S3FS) invalidateCaches() {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.statfsCache = nil
	fs.listCache = nil
	fs.listCacheTime = time.Time{}
	fmt.Printf("[Cache] *** CACHES INVALIDATED ***\n")
}

// getListObjects obtiene lista de objetos con cache
func (fs *S3FS) getListObjects(ctx context.Context) ([]storage.ObjectInfo, error) {
	fs.mu.RLock()
	if fs.listCache != nil && time.Since(fs.listCacheTime) < fs.listCacheTTL {
		cached := fs.listCache
		fs.mu.RUnlock()
		fmt.Printf("[Cache] Using cached object list (%d objects)\n", len(cached))
		return cached, nil
	}
	fs.mu.RUnlock()

	// Obtener de S3
	objects, err := fs.s3Client.ListObjects(ctx, fs.bucketName, "")
	if err != nil {
		return nil, err
	}

	// Guardar en cache
	fs.mu.Lock()
	fs.listCache = objects
	fs.listCacheTime = time.Now()
	fs.mu.Unlock()

	fmt.Printf("[Cache] Cached new object list (%d objects)\n", len(objects))
	return objects, nil
}

// Statfs obtiene información del filesystem
func (fs *S3FS) Statfs(path string, stat *cgofuse.Statfs_t) int {
	fmt.Printf("[Statfs] path='%s'\n", path)

	fs.mu.RLock()
	// Verificar cache
	if fs.statfsCache != nil && time.Since(fs.statfsCacheTime) < fs.statfsCacheTTL {
		*stat = *fs.statfsCache
		totalBytes := stat.Blocks * stat.Bsize
		usedBytes := (stat.Blocks - stat.Bfree) * stat.Bsize
		fs.mu.RUnlock()
		fmt.Printf("[Statfs] Using cached value: Total=%d GB, Used=%d MB\n",
			totalBytes/(1024*1024*1024), usedBytes/(1024*1024))
		return 0
	}
	fs.mu.RUnlock()

	// Calcular tamaño total del bucket
	ctx := context.Background()
	objects, err := fs.s3Client.ListObjects(ctx, fs.bucketName, "")
	if err != nil {
		fmt.Printf("[Statfs] Error listing objects: %v\n", err)
		// Valores por defecto si hay error
		stat.Bsize = 4096
		stat.Frsize = 4096
		stat.Blocks = 1000000000 // ~4TB
		stat.Bfree = 500000000
		stat.Bavail = 500000000
		stat.Files = 1000000
		stat.Ffree = 1000000
		stat.Favail = 1000000
		stat.Fsid = 0
		stat.Flag = 0 // No readonly
		stat.Namemax = 255
		return 0
	}

	// Calcular tamaño usado
	var totalSize int64
	var fileCount int64
	for _, obj := range objects {
		if !obj.IsDir {
			totalSize += obj.Size
			fileCount++
		}
	}

	const blockSize = 4096
	usedBlocks := uint64(totalSize / blockSize)
	if totalSize%blockSize != 0 {
		usedBlocks++
	}

	// Calcular tamaño total como: usado + 10GB disponible
	availableSize := uint64(10 * 1024 * 1024 * 1024) // 10GB disponible
	availableBlocks := availableSize / blockSize
	totalBlocks := usedBlocks + availableBlocks

	stat.Bsize = blockSize
	stat.Frsize = blockSize
	stat.Blocks = totalBlocks
	stat.Bfree = availableBlocks
	stat.Bavail = availableBlocks
	stat.Files = uint64(fileCount) + 100000
	stat.Ffree = 100000
	stat.Favail = 100000
	stat.Fsid = 0
	stat.Flag = 0 // No readonly
	stat.Namemax = 255

	// Guardar en cache
	fs.mu.Lock()
	cached := *stat
	fs.statfsCache = &cached
	fs.statfsCacheTime = time.Now()
	fs.mu.Unlock()

	totalBytes := stat.Blocks * stat.Bsize
	usedBytes := (stat.Blocks - stat.Bfree) * stat.Bsize
	fmt.Printf("[Statfs] *** NUEVO CALCULO ***\n")
	fmt.Printf("[Statfs] Archivos en bucket: %d archivos = %d MB\n", fileCount, totalSize/(1024*1024))
	fmt.Printf("[Statfs] Tamaño total volumen: %d GB\n", totalBytes/(1024*1024*1024))
	fmt.Printf("[Statfs] Espacio usado: %d MB\n", usedBytes/(1024*1024))
	fmt.Printf("[Statfs] Espacio libre: %d GB\n", (stat.Bfree*stat.Bsize)/(1024*1024*1024))
	return 0
}

// Open abre un archivo
func (fs *S3FS) Open(path string, flags int) (int, uint64) {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Open] path='%s' flags=%d\n", path, flags)

	// Si es solo lectura, no crear file handle especial
	isWrite := (flags&cgofuse.O_WRONLY != 0) || (flags&cgofuse.O_RDWR != 0)
	if !isWrite {
		fmt.Printf("[Open] Read-only mode\n")
		return 0, 0
	}

	// Modo escritura: crear archivo temporal
	fs.mu.Lock()
	defer fs.mu.Unlock()

	fh := fs.nextFh
	fs.nextFh++

	// Crear archivo temporal
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("maxiofs-%d.tmp", fh))

	var fileSize int64 = 0

	// Si el archivo existe en S3, descargarlo al temp
	ctx := context.Background()
	reader, size, err := fs.s3Client.GetObject(ctx, fs.bucketName, path)
	if err == nil && reader != nil {
		tmpF, err := os.Create(tempFile)
		if err == nil {
			io.Copy(tmpF, reader)
			tmpF.Close()
			fileSize = size
			fmt.Printf("[Open] Downloaded existing file to temp, size: %d\n", size)
		}
		reader.Close()
	} else {
		// Crear archivo temporal vacío
		tmpF, err := os.Create(tempFile)
		if err == nil {
			tmpF.Close()
		}
		fmt.Printf("[Open] Created empty temp file\n")
	}

	fs.openFiles[fh] = &OpenFile{
		Path:     path,
		TempFile: tempFile,
		Size:     fileSize,
		Dirty:    false,
	}

	fmt.Printf("[Open] Created file handle %d with temp file %s\n", fh, tempFile)
	return 0, fh
}

// Flush sincroniza datos al storage
func (fs *S3FS) Flush(path string, fh uint64) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Flush] path='%s' fh=%d\n", path, fh)

	fs.mu.Lock()
	openFile, exists := fs.openFiles[fh]
	if !exists || !openFile.Dirty {
		fs.mu.Unlock()
		fmt.Printf("[Flush] Nothing to flush\n")
		return 0
	}

	tempFile := openFile.TempFile
	filePath := openFile.Path
	fs.mu.Unlock()

	// Subir archivo temporal a S3 usando UploadFile del SDK
	ctx := context.Background()
	fmt.Printf("[Flush] Uploading temp file %s to S3: %s\n", tempFile, filePath)

	err := fs.s3Client.UploadFile(ctx, fs.bucketName, filePath, tempFile)
	if err != nil {
		fmt.Printf("[Flush] Error uploading: %v\n", err)
		return -cgofuse.EIO
	}

	// Marcar como no dirty
	fs.mu.Lock()
	if openFile, exists := fs.openFiles[fh]; exists {
		openFile.Dirty = false
	}
	fs.mu.Unlock()

	// Invalidar TODOS los caches para forzar refresh
	fs.invalidateCaches()

	fmt.Printf("[Flush] Successfully uploaded to S3 and invalidated caches\n")
	return 0
}

// Release cierra un archivo
func (fs *S3FS) Release(path string, fh uint64) int {
	fmt.Printf("[Release] *** CLOSING FILE *** path='%s' fh=%d\n", path, fh)

	// Verificar si hay datos pendientes
	fs.mu.RLock()
	openFile, exists := fs.openFiles[fh]
	var tempFile string
	if exists {
		fmt.Printf("[Release] File size: %d bytes, dirty=%v\n", openFile.Size, openFile.Dirty)
		tempFile = openFile.TempFile
	}
	fs.mu.RUnlock()

	// Flush antes de cerrar
	result := fs.Flush(path, fh)
	if result != 0 {
		fmt.Printf("[Release] *** ERROR *** Flush failed with code %d\n", result)
	}

	// Eliminar archivo temporal
	if tempFile != "" {
		os.Remove(tempFile)
		fmt.Printf("[Release] Deleted temp file: %s\n", tempFile)
	}

	// Limpiar file handle
	fs.mu.Lock()
	delete(fs.openFiles, fh)
	fs.mu.Unlock()

	fmt.Printf("[Release] *** FILE CLOSED *** handle %d\n", fh)
	return 0
}

// Opendir abre un directorio para lectura
func (fs *S3FS) Opendir(path string) (int, uint64) {
	fmt.Printf("[Opendir] path='%s'\n", path)
	return 0, 0
}

// Releasedir cierra un directorio
func (fs *S3FS) Releasedir(path string, fh uint64) int {
	fmt.Printf("[Releasedir] path='%s' fh=%d\n", path, fh)
	return 0
}

// Getattr obtiene atributos de un archivo/directorio
func (fs *S3FS) Getattr(path string, stat *cgofuse.Stat_t, fh uint64) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Getattr] path='%s' fh=%d\n", path, fh)

	// Root
	if path == "" {
		stat.Mode = cgofuse.S_IFDIR | 0777 // Todos los permisos
		stat.Nlink = 2
		stat.Uid = 0
		stat.Gid = 0
		now := time.Now().Unix()
		stat.Atim.Sec = now
		stat.Mtim.Sec = now
		stat.Ctim.Sec = now
		fmt.Printf("[Getattr] Returning root directory (mode=0777)\n")
		return 0
	}

	// PRIMERO: Verificar si existe un archivo abierto en escritura
	fs.mu.RLock()
	for _, openFile := range fs.openFiles {
		if openFile.Path == path {
			fmt.Printf("[Getattr] *** FOUND OPEN FILE *** path='%s' size=%d\n", path, openFile.Size)
			stat.Mode = cgofuse.S_IFREG | 0666
			stat.Size = openFile.Size
			stat.Uid = 0
			stat.Gid = 0
			now := time.Now().Unix()
			stat.Atim.Sec = now
			stat.Mtim.Sec = now
			stat.Ctim.Sec = now
			fs.mu.RUnlock()
			return 0
		}
	}
	fs.mu.RUnlock()

	ctx := context.Background()

	// Buscar coincidencia exacta en S3
	objects, err := fs.getListObjects(ctx)
	if err != nil {
		fmt.Printf("[Getattr] Error listing objects: %v\n", err)
		return -cgofuse.ENOENT
	}

	fmt.Printf("[Getattr] Checking %d objects in S3\n", len(objects))

	// Buscar coincidencia exacta
	for _, obj := range objects {
		objPath := strings.TrimPrefix(obj.Key, "/")
		if objPath == path || objPath == path+"/" {
			fmt.Printf("[Getattr] Found exact match: %s (IsDir=%v, Size=%d)\n", obj.Key, obj.IsDir, obj.Size)
			if obj.IsDir {
				stat.Mode = cgofuse.S_IFDIR | 0777
			} else {
				stat.Mode = cgofuse.S_IFREG | 0666
				stat.Size = obj.Size
				stat.Mtim.Sec = obj.LastModified.Unix()
			}
			stat.Uid = 0
			stat.Gid = 0
			return 0
		}
	}

	// Verificar si es un directorio implícito (tiene hijos)
	pathPrefix := path + "/"
	for _, obj := range objects {
		if strings.HasPrefix(obj.Key, pathPrefix) {
			fmt.Printf("[Getattr] Found implicit directory: %s\n", path)
			stat.Mode = cgofuse.S_IFDIR | 0777
			stat.Uid = 0
			stat.Gid = 0
			return 0
		}
	}

	fmt.Printf("[Getattr] Not found: %s\n", path)
	return -cgofuse.ENOENT
}

// Readdir lee el contenido de un directorio
func (fs *S3FS) Readdir(path string,
	fill func(name string, stat *cgofuse.Stat_t, ofst int64) bool,
	ofst int64,
	fh uint64) int {

	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Readdir] path=%s\n", path)

	ctx := context.Background()
	objects, err := fs.getListObjects(ctx)
	if err != nil {
		fmt.Printf("[Readdir] Error listing objects: %v\n", err)
		return -cgofuse.ENOENT
	}

	fmt.Printf("[Readdir] Processing %d objects\n", len(objects))

	fill(".", nil, 0)
	fill("..", nil, 0)

	// Mapa para evitar duplicados
	seen := make(map[string]bool)

	// Prefijo del directorio actual
	var prefix string
	if path != "" {
		prefix = path + "/"
	}

	for _, obj := range objects {
		objKey := strings.TrimPrefix(obj.Key, "/")

		// Si estamos en root, mostrar todo
		// Si no, solo mostrar items que empiecen con el prefijo
		if prefix != "" && !strings.HasPrefix(objKey, prefix) {
			continue
		}

		// Obtener la parte relativa
		relativePath := objKey
		if prefix != "" {
			relativePath = strings.TrimPrefix(objKey, prefix)
		}

		// Si está vacío o es el mismo directorio, skip
		if relativePath == "" || relativePath == "/" {
			continue
		}

		// Si contiene /, es un subdirectorio
		var name string
		isDir := false
		if idx := strings.Index(relativePath, "/"); idx > 0 {
			name = relativePath[:idx]
			isDir = true
		} else {
			name = relativePath
			isDir = obj.IsDir
		}

		// Evitar duplicados
		if seen[name] {
			continue
		}
		seen[name] = true

		fmt.Printf("[Readdir] Adding: %s (isDir=%v)\n", name, isDir)

		var stat cgofuse.Stat_t
		if isDir {
			stat.Mode = cgofuse.S_IFDIR | 0777
		} else {
			stat.Mode = cgofuse.S_IFREG | 0666
			stat.Size = obj.Size
			stat.Mtim.Sec = obj.LastModified.Unix()
		}
		stat.Uid = 0
		stat.Gid = 0

		fill(name, &stat, 0)
	}

	return 0
}

// Read lee datos de un archivo
func (fs *S3FS) Read(path string, buff []byte, ofst int64, fh uint64) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Read] path=%s offset=%d len=%d\n", path, ofst, len(buff))

	ctx := context.Background()
	reader, size, err := fs.s3Client.GetObject(ctx, fs.bucketName, path)
	if err != nil {
		fmt.Printf("[Read] Error getting object: %v\n", err)
		return -cgofuse.EIO
	}
	defer reader.Close()

	fmt.Printf("[Read] Object size: %d\n", size)

	// Verificar si el offset está fuera de rango
	if ofst >= size {
		return 0
	}

	// Seek al offset descartando bytes
	if ofst > 0 {
		discarded, err := io.CopyN(io.Discard, reader, ofst)
		if err != nil {
			fmt.Printf("[Read] Error seeking to offset: %v\n", err)
			return -cgofuse.EIO
		}
		fmt.Printf("[Read] Discarded %d bytes to reach offset\n", discarded)
	}

	// Leer datos
	n, err := io.ReadFull(reader, buff)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		fmt.Printf("[Read] Error reading: %v\n", err)
		return -cgofuse.EIO
	}

	fmt.Printf("[Read] Read %d bytes\n", n)
	return n
}

// Write escribe datos a un archivo
func (fs *S3FS) Write(path string, buff []byte, ofst int64, fh uint64) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Write] *** WRITING DATA *** path='%s' offset=%d len=%d fh=%d\n", path, ofst, len(buff), fh)

	fs.mu.RLock()
	openFile, exists := fs.openFiles[fh]
	if !exists {
		fs.mu.RUnlock()
		fmt.Printf("[Write] *** ERROR *** File handle not found: %d\n", fh)
		return -cgofuse.EBADF
	}
	tempFile := openFile.TempFile
	fs.mu.RUnlock()

	// Abrir archivo temporal para escribir
	f, err := os.OpenFile(tempFile, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		fmt.Printf("[Write] *** ERROR *** Cannot open temp file: %v\n", err)
		return -cgofuse.EIO
	}
	defer f.Close()

	// Escribir en el offset correcto
	_, err = f.WriteAt(buff, ofst)
	if err != nil {
		fmt.Printf("[Write] *** ERROR *** Cannot write to temp file: %v\n", err)
		return -cgofuse.EIO
	}

	// Actualizar tamaño
	newSize := ofst + int64(len(buff))
	fs.mu.Lock()
	if openFile, exists := fs.openFiles[fh]; exists {
		if newSize > openFile.Size {
			openFile.Size = newSize
		}
		openFile.Dirty = true
	}
	fs.mu.Unlock()

	fmt.Printf("[Write] *** SUCCESS *** Written %d bytes to temp file at offset %d\n", len(buff), ofst)
	return len(buff)
}

// Create crea un archivo
func (fs *S3FS) Create(path string, flags int, mode uint32) (int, uint64) {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Create] *** CREATING FILE ***\n")
	fmt.Printf("[Create] path='%s' flags=%d mode=%o\n", path, flags, mode)

	fs.mu.Lock()
	defer fs.mu.Unlock()

	// Crear nuevo file handle
	fh := fs.nextFh
	fs.nextFh++

	// Crear archivo temporal
	tempDir := os.TempDir()
	tempFile := filepath.Join(tempDir, fmt.Sprintf("maxiofs-%d.tmp", fh))

	// Crear archivo vacío
	tmpF, err := os.Create(tempFile)
	if err != nil {
		fmt.Printf("[Create] *** ERROR *** Cannot create temp file: %v\n", err)
		return -cgofuse.EIO, ^uint64(0)
	}
	tmpF.Close()

	fs.openFiles[fh] = &OpenFile{
		Path:     path,
		TempFile: tempFile,
		Size:     0,
		Dirty:    false,
	}

	fmt.Printf("[Create] *** SUCCESS *** File handle %d created with temp file %s\n", fh, tempFile)
	return 0, fh
}

// Unlink elimina un archivo
func (fs *S3FS) Unlink(path string) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Unlink] path='%s'\n", path)

	ctx := context.Background()
	err := fs.s3Client.DeleteObject(ctx, fs.bucketName, path)
	if err != nil {
		fmt.Printf("[Unlink] Error deleting: %v\n", err)
		return -cgofuse.EIO
	}

	// Invalidar TODOS los caches
	fs.invalidateCaches()

	fmt.Printf("[Unlink] Successfully deleted: %s\n", path)
	return 0
}

// Mkdir crea un directorio
func (fs *S3FS) Mkdir(path string, mode uint32) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Mkdir] path='%s' mode=%o\n", path, mode)

	// En S3, los directorios son implícitos cuando se crean archivos dentro
	// Pero algunos clientes esperan poder crear directorios vacíos
	// Crear un marcador de directorio (objeto que termina en /)
	ctx := context.Background()
	err := fs.s3Client.UploadData(ctx, fs.bucketName, path+"/", []byte{})
	if err != nil {
		fmt.Printf("[Mkdir] Error creating directory marker: %v\n", err)
		return -cgofuse.EIO
	}

	// Invalidar TODOS los caches
	fs.invalidateCaches()

	fmt.Printf("[Mkdir] Directory created: %s\n", path)
	return 0
}

// Rmdir elimina un directorio
func (fs *S3FS) Rmdir(path string) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Rmdir] path='%s'\n", path)

	// Verificar que el directorio esté vacío
	ctx := context.Background()
	objects, err := fs.s3Client.ListObjects(ctx, fs.bucketName, path+"/")
	if err != nil {
		fmt.Printf("[Rmdir] Error listing: %v\n", err)
		return -cgofuse.EIO
	}

	if len(objects) > 0 {
		fmt.Printf("[Rmdir] Directory not empty\n")
		return -cgofuse.ENOTEMPTY
	}

	// Eliminar marcador de directorio si existe
	fs.s3Client.DeleteObject(ctx, fs.bucketName, path+"/")

	// Invalidar TODOS los caches
	fs.invalidateCaches()

	fmt.Printf("[Rmdir] Directory removed\n")
	return 0
}

// Access verifica permisos de acceso
func (fs *S3FS) Access(path string, mask uint32) int {
	fmt.Printf("[Access] path='%s' mask=%d\n", path, mask)
	// Siempre permitir acceso
	return 0
}

// Rename renombra un archivo o directorio
func (fs *S3FS) Rename(oldpath string, newpath string) int {
	oldpath = strings.TrimPrefix(oldpath, "/")
	newpath = strings.TrimPrefix(newpath, "/")
	fmt.Printf("[Rename] from='%s' to='%s'\n", oldpath, newpath)

	ctx := context.Background()

	// Verificar si es un directorio
	objects, err := fs.s3Client.ListObjects(ctx, fs.bucketName, "")
	if err != nil {
		fmt.Printf("[Rename] Error listing: %v\n", err)
		return -cgofuse.EIO
	}

	isDir := false
	var filesToMove []string

	// Verificar si oldpath es un directorio mirando si existe oldpath/
	for _, obj := range objects {
		if obj.Key == oldpath+"/" {
			isDir = true
		}
		// Recoger todos los archivos que empiezan con oldpath/
		if strings.HasPrefix(obj.Key, oldpath+"/") {
			filesToMove = append(filesToMove, obj.Key)
		}
	}

	// Si es directorio, mover todos los archivos
	if isDir || len(filesToMove) > 0 {
		fmt.Printf("[Rename] Moving directory with %d items using S3 CopyObject\n", len(filesToMove))
		for _, oldKey := range filesToMove {
			// Reemplazar prefijo
			newKey := strings.Replace(oldKey, oldpath+"/", newpath+"/", 1)

			// Copiar usando S3 CopyObject (server-side, eficiente)
			err = fs.s3Client.CopyObject(ctx, fs.bucketName, oldKey, newKey)
			if err != nil {
				fmt.Printf("[Rename] Error copying %s to %s: %v\n", oldKey, newKey, err)
				return -cgofuse.EIO
			}

			// Eliminar original
			fs.s3Client.DeleteObject(ctx, fs.bucketName, oldKey)
			fmt.Printf("[Rename] Moved %s -> %s\n", oldKey, newKey)
		}

		// Crear marcador de directorio nuevo si no hay archivos
		if len(filesToMove) == 0 {
			err = fs.s3Client.UploadData(ctx, fs.bucketName, newpath+"/", []byte{})
			if err != nil {
				fmt.Printf("[Rename] Error creating new dir marker: %v\n", err)
				return -cgofuse.EIO
			}
			// Eliminar marcador viejo
			fs.s3Client.DeleteObject(ctx, fs.bucketName, oldpath+"/")
		}
	} else {
		// Es un archivo simple
		fmt.Printf("[Rename] Moving single file using S3 CopyObject\n")

		// Copiar usando S3 CopyObject (server-side)
		err = fs.s3Client.CopyObject(ctx, fs.bucketName, oldpath, newpath)
		if err != nil {
			fmt.Printf("[Rename] Error copying file: %v\n", err)
			return -cgofuse.EIO
		}

		// Eliminar original
		err = fs.s3Client.DeleteObject(ctx, fs.bucketName, oldpath)
		if err != nil {
			fmt.Printf("[Rename] Error deleting old file: %v\n", err)
			// No retornar error aquí, el archivo ya se copió
		}
		fmt.Printf("[Rename] Moved %s -> %s\n", oldpath, newpath)
	}

	// Invalidar TODOS los caches
	fs.invalidateCaches()

	fmt.Printf("[Rename] Rename completed successfully\n")
	return 0
}

// Truncate cambia el tamaño de un archivo
func (fs *S3FS) Truncate(path string, size int64, fh uint64) int {
	path = strings.TrimPrefix(path, "/")
	fmt.Printf("[Truncate] *** TRUNCATE CALLED *** path='%s' size=%d fh=%d\n", path, size, fh)

	// Si tenemos file handle, truncar el archivo temporal
	if fh != ^uint64(0) {
		fs.mu.RLock()
		openFile, exists := fs.openFiles[fh]
		if !exists {
			fs.mu.RUnlock()
			return -cgofuse.EBADF
		}
		tempFile := openFile.TempFile
		fs.mu.RUnlock()

		// Truncar archivo temporal
		err := os.Truncate(tempFile, size)
		if err != nil {
			fmt.Printf("[Truncate] Error truncating temp file: %v\n", err)
			return -cgofuse.EIO
		}

		fs.mu.Lock()
		if openFile, exists := fs.openFiles[fh]; exists {
			openFile.Size = size
			openFile.Dirty = true
		}
		fs.mu.Unlock()

		fmt.Printf("[Truncate] Temp file truncated to %d bytes\n", size)
		return 0
	}

	// Sin file handle: truncar archivo en S3
	if size == 0 {
		// Truncar a 0: crear archivo vacío
		ctx := context.Background()
		err := fs.s3Client.UploadData(ctx, fs.bucketName, path, []byte{})
		if err != nil {
			fmt.Printf("[Truncate] Error creating empty file: %v\n", err)
			return -cgofuse.EIO
		}
		fmt.Printf("[Truncate] Created empty file in S3\n")
		return 0
	}

	fmt.Printf("[Truncate] Non-zero truncate without fh not supported\n")
	return -cgofuse.ENOSYS
}
