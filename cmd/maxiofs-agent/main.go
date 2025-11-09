package main

import (
	"context"
	_ "embed"
	"fmt"
	"sync"

	"maxiofs-agent/internal/cgofuse"
	"maxiofs-agent/internal/config"
	"maxiofs-agent/internal/storage"
	"maxiofs-agent/internal/vfs"

	"github.com/gen2brain/dlgs"
	"github.com/getlantern/systray"
)

//go:embed icon.ico
var iconData []byte

type App struct {
	config         *config.Config
	s3Client       *storage.S3Client
	mountedBuckets map[string]*MountedBucket
	mu             sync.Mutex

	// Menu items
	statusItem     *systray.MenuItem
	connectItem    *systray.MenuItem
	disconnectItem *systray.MenuItem
	bucketsMenu    *systray.MenuItem
}

type MountedBucket struct {
	BucketName  string
	DriveLetter string
	Host        *cgofuse.FileSystemHost
}

var app *App

func main() {
	app = &App{
		mountedBuckets: make(map[string]*MountedBucket),
	}

	// Cargar configuración
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{UseSSL: true}
	}
	app.config = cfg

	// Iniciar systray
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(iconData)
	systray.SetTitle("MaxIOFS")
	systray.SetTooltip("MaxIOFS Agent - Montaje de Buckets S3")

	// Status
	app.statusItem = systray.AddMenuItem("⚫ Desconectado", "Estado")
	app.statusItem.Disable()

	systray.AddSeparator()

	// Conectar
	app.connectItem = systray.AddMenuItem("🔌 Configurar Conexión", "Configurar credenciales de MaxIOFS")

	// Desconectar
	app.disconnectItem = systray.AddMenuItem("🔴 Desconectar", "Desconectar de MaxIOFS")
	app.disconnectItem.Disable()
	app.disconnectItem.Hide()

	systray.AddSeparator()

	// Buckets
	app.bucketsMenu = systray.AddMenuItem("📦 Buckets", "Ver y montar buckets")
	app.bucketsMenu.Disable()

	systray.AddSeparator()

	// Ayuda
	helpItem := systray.AddMenuItem("❓ Ayuda", "Cómo usar")

	systray.AddSeparator()

	// Salir
	quitItem := systray.AddMenuItem("❌ Salir", "Cerrar MaxIOFS Agent")

	// Auto-conectar
	if app.config.Endpoint != "" && app.config.AccessKeyID != "" && app.config.SecretAccessKey != "" {
		tryConnect()
	}

	// Event loop
	go func() {
		for {
			select {
			case <-app.connectItem.ClickedCh:
				go showSettings()
			case <-app.disconnectItem.ClickedCh:
				go disconnect()
			case <-helpItem.ClickedCh:
				go showHelp()
			case <-quitItem.ClickedCh:
				disconnect()
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	disconnect()
}

func showSettings() {
	endpoint, ok, _ := dlgs.Entry("MaxIOFS - Endpoint", "Servidor (ej: localhost:9000):", app.config.Endpoint)
	if !ok || endpoint == "" {
		return
	}

	accessKey, ok, _ := dlgs.Entry("MaxIOFS - Access Key", "Tu Access Key ID:", app.config.AccessKeyID)
	if !ok || accessKey == "" {
		return
	}

	secretKey, ok, _ := dlgs.Password("MaxIOFS - Secret Key", "Tu Secret Access Key:")
	if !ok || secretKey == "" {
		return
	}

	useSSL, _ := dlgs.Question("MaxIOFS - SSL/TLS", "¿Usar conexión segura (SSL/TLS)?", app.config.UseSSL)

	app.config.Endpoint = endpoint
	app.config.AccessKeyID = accessKey
	app.config.SecretAccessKey = secretKey
	app.config.UseSSL = useSSL
	app.config.Save()

	tryConnect()
}

func tryConnect() {
	go func() {
		app.statusItem.SetTitle("🟡 Conectando...")

		client, err := storage.NewS3Client(
			app.config.Endpoint,
			app.config.AccessKeyID,
			app.config.SecretAccessKey,
			app.config.UseSSL,
		)
		if err != nil {
			app.statusItem.SetTitle("⚫ Error de conexión")
			dlgs.Error("Error", "No se pudo conectar: "+err.Error())
			return
		}

		ctx := context.Background()
		if err := client.TestConnection(ctx); err != nil {
			app.statusItem.SetTitle("⚫ Error de conexión")
			dlgs.Error("Error", "No se pudo conectar: "+err.Error())
			return
		}

		app.mu.Lock()
		app.s3Client = client
		app.mu.Unlock()

		app.statusItem.SetTitle("🟢 Conectado - " + app.config.Endpoint)
		app.connectItem.Disable()
		app.disconnectItem.Enable()
		app.disconnectItem.Show()

		loadBuckets()
		dlgs.Info("Conexión Exitosa", "Conectado a MaxIOFS")
	}()
}

func disconnect() {
	app.mu.Lock()
	defer app.mu.Unlock()

	// Desmontar todos los buckets
	for _, mounted := range app.mountedBuckets {
		if mounted.Host != nil {
			mounted.Host.Unmount()
		}
	}
	app.mountedBuckets = make(map[string]*MountedBucket)

	app.s3Client = nil
	app.statusItem.SetTitle("⚫ Desconectado")
	app.connectItem.Enable()
	app.disconnectItem.Disable()
	app.disconnectItem.Hide()
	app.bucketsMenu.Disable()
}

func loadBuckets() {
	if app.s3Client == nil {
		return
	}

	ctx := context.Background()
	buckets, err := app.s3Client.ListBuckets(ctx)
	if err != nil {
		dlgs.Error("Error", "Error listando buckets: "+err.Error())
		return
	}

	app.bucketsMenu.Enable()

	for _, bucket := range buckets {
		bucketName := bucket.Name
		item := app.bucketsMenu.AddSubMenuItem("📦 "+bucketName, "Click para montar como unidad")

		go func(name string, menuItem *systray.MenuItem) {
			for {
				<-menuItem.ClickedCh
				toggleBucketMount(name, menuItem)
			}
		}(bucketName, item)
	}
}

func toggleBucketMount(bucketName string, menuItem *systray.MenuItem) {
	app.mu.Lock()

	// Si ya está montado, desmontar
	if mounted, exists := app.mountedBuckets[bucketName]; exists {
		if mounted.Host != nil {
			mounted.Host.Unmount()
		}
		delete(app.mountedBuckets, bucketName)
		app.mu.Unlock()

		menuItem.SetTitle("📦 " + bucketName)
		dlgs.Info("Desmontado", "Bucket desmontado correctamente")
		return
	}
	app.mu.Unlock()

	// Solicitar letra de unidad
	driveLetter, ok, _ := dlgs.Entry(
		"Montar Bucket",
		"Letra de unidad (ej: Z):",
		"Z",
	)
	if !ok || driveLetter == "" {
		return
	}
	driveLetter = driveLetter[:1] // Solo primera letra
	mountPoint := driveLetter + ":"

	// Crear filesystem
	fs := vfs.NewS3FS(app.s3Client, bucketName)
	host := cgofuse.NewFileSystemHost(fs)

	// Habilitar capacidades de escritura
	host.SetCapCaseInsensitive(false)
	host.SetCapReaddirPlus(false)

	// Opciones de montaje simplificadas
	mountOpts := []string{
		"-o", "volname=" + bucketName,
		"-o", "umask=0",
	}

	fmt.Printf("Montando bucket '%s' en '%s' con permisos de escritura...\n", bucketName, mountPoint)

	// Montar en goroutine
	go func() {
		if !host.Mount(mountPoint, mountOpts) {
			dlgs.Error("Error", fmt.Sprintf("No se pudo montar el bucket '%s' en '%s'", bucketName, mountPoint))
			return
		}
		fmt.Printf("Montaje completado para %s\n", bucketName)
	}()

	// Guardar referencia
	app.mu.Lock()
	app.mountedBuckets[bucketName] = &MountedBucket{
		BucketName:  bucketName,
		DriveLetter: driveLetter,
		Host:        host,
	}
	app.mu.Unlock()

	menuItem.SetTitle("✅ " + bucketName + " (" + driveLetter + ":)")
	dlgs.Info("Montado", fmt.Sprintf("Bucket '%s' montado en %s:\n\nAccede desde el Explorador de Windows", bucketName, driveLetter+":"))
}

func showHelp() {
	dlgs.Info("Ayuda - MaxIOFS Agent",
		"Cómo usar:\n\n"+
			"1. Configurar Conexión → Ingresar credenciales\n"+
			"2. Buckets → Click en un bucket\n"+
			"3. Elegir letra de unidad (ej: Z)\n"+
			"4. ¡Listo! Accede desde el Explorador\n\n"+
			"Los archivos se cargan bajo demanda.\n"+
			"No descarga todo el bucket.")
}
