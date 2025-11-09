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

	"fyne.io/fyne/v2"
	fyneapp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"fyne.io/systray"
	"github.com/gen2brain/dlgs"
)

//go:embed icon.ico
var iconData []byte

//go:embed icon.png
var iconPNG []byte

type App struct {
	config         *config.Config
	s3Client       *storage.S3Client
	mountedBuckets map[string]*MountedBucket
	mu             sync.Mutex

	// Fyne app for windows
	fyneApp fyne.App

	// Menu items
	statusItem     *systray.MenuItem
	connectItem    *systray.MenuItem
	disconnectItem *systray.MenuItem
	bucketsMenu    *systray.MenuItem
	bucketItems    []*systray.MenuItem // Para trackear los items de buckets
}

type MountedBucket struct {
	BucketName  string
	DriveLetter string
	Host        *cgofuse.FileSystemHost
}

var app *App

func main() {
	// Create Fyne app first
	fyneApp := fyneapp.NewWithID("com.maxiofs.agent")

	// Configurar icono de la aplicación (usar PNG para Fyne)
	fyneApp.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))

	app = &App{
		mountedBuckets: make(map[string]*MountedBucket),
		fyneApp:        fyneApp,
	}

	// Cargar configuración
	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{UseSSL: true}
	}
	app.config = cfg

	// Crear una ventana invisible para mantener la app viva
	// Esto evita que Fyne cierre la app cuando todas las ventanas visibles se cierran
	dummyWindow := fyneApp.NewWindow("")
	dummyWindow.Resize(fyne.NewSize(1, 1))
	dummyWindow.Hide()

	// Iniciar systray en una goroutine
	go systray.Run(onReady, onExit)

	// Iniciar el bucle de eventos de Fyne (esto debe ser en el thread principal)
	fyneApp.Run()
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
				go confirmQuit()
			}
		}
	}()
}

func onExit() {
	disconnect()
}

func showSettings() {
	// Usar Do (no DoAndWait) para no bloquear
	fyne.Do(func() {
		// Usar la app Fyne existente, NO crear una nueva
		window := app.fyneApp.NewWindow("MaxIOFS - Connection Settings")
		window.SetIcon(fyne.NewStaticResource("icon.png", iconPNG))
		window.SetFixedSize(true)

		// Create form fields
		endpointEntry := widget.NewEntry()
		endpointEntry.SetPlaceHolder("e.g., localhost:9000 or s3.example.com")
		endpointEntry.SetText(app.config.Endpoint)

		accessKeyEntry := widget.NewEntry()
		accessKeyEntry.SetPlaceHolder("Your Access Key ID")
		accessKeyEntry.SetText(app.config.AccessKeyID)

		secretKeyEntry := widget.NewPasswordEntry()
		secretKeyEntry.SetPlaceHolder("Your Secret Access Key")
		secretKeyEntry.SetText(app.config.SecretAccessKey)

		sslCheck := widget.NewCheck("Use SSL/TLS (Secure Connection)", nil)
		sslCheck.SetChecked(app.config.UseSSL)

		// Create form
		form := container.NewVBox(
			widget.NewLabel("Endpoint:"),
			endpointEntry,
			widget.NewLabel("Access Key ID:"),
			accessKeyEntry,
			widget.NewLabel("Secret Access Key:"),
			secretKeyEntry,
			widget.NewLabel(""),
			sslCheck,
		)

		// Create buttons
		saveBtn := widget.NewButton("Connect", func() {
			endpoint := endpointEntry.Text
			accessKey := accessKeyEntry.Text
			secretKey := secretKeyEntry.Text
			useSSL := sslCheck.Checked

			if endpoint == "" || accessKey == "" || secretKey == "" {
				dialog.ShowError(fmt.Errorf("All fields are required"), window)
				return
			}

			app.config.Endpoint = endpoint
			app.config.AccessKeyID = accessKey
			app.config.SecretAccessKey = secretKey
			app.config.UseSSL = useSSL
			app.config.Save()

			window.Close()
			go tryConnect()
		})

		cancelBtn := widget.NewButton("Cancel", func() {
			window.Close()
		})

		buttons := container.NewGridWithColumns(2, cancelBtn, saveBtn)

		// Layout
		content := container.NewVBox(
			widget.NewLabelWithStyle("Connection Settings", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			form,
			widget.NewSeparator(),
			buttons,
		)

		window.SetContent(container.NewPadded(content))
		window.CenterOnScreen()
		window.Show() // Usar Show() en lugar de ShowAndRun()
	})
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

	// Ocultar y limpiar items de buckets
	for _, item := range app.bucketItems {
		item.Hide()
	}
	app.bucketItems = nil

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

	// Limpiar items anteriores
	for _, item := range app.bucketItems {
		item.Hide()
	}
	app.bucketItems = nil

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
		app.bucketItems = append(app.bucketItems, item) // Trackear el item

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

func confirmQuit() {
	ok, _ := dlgs.Question("Salir", "¿Está seguro que desea salir de MaxIOFS Agent?", false)
	if ok {
		disconnect()
		systray.Quit()
		app.fyneApp.Quit()
	}
}
