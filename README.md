# MaxIOFS Desktop Agent

Agente de escritorio para montar buckets de MaxIOFS como unidades locales.

## Características

- ✅ Interfaz gráfica con icono en bandeja del sistema (system tray)
- ✅ Monta buckets S3/MaxIOFS como unidades de disco
- ✅ Soporte cross-platform (Windows, Linux, macOS)
- ✅ Configuración visual con diálogos nativos
- ✅ Gestión de múltiples buckets
- ✅ Auto-conexión al iniciar

## Requisitos

### Windows
- **WinFsp** instalado (https://github.com/winfsp/winfsp/releases)
  - Descarga e instala `winfsp-x.x.xxxxx.msi`

### Linux
- **FUSE** instalado
  ```bash
  sudo apt install fuse libfuse2  # Debian/Ubuntu
  sudo yum install fuse           # RedHat/CentOS
  ```

### macOS
- **macFUSE** instalado (https://osxfuse.github.io/)

## Uso

1. **Ejecutar el agente**
   ```bash
   ./maxiofs-agent.exe
   ```

2. **Aparecerá un icono en la bandeja del sistema**

3. **Configurar conexión**
   - Click derecho en el icono
   - Seleccionar "Configurar y Conectar"
   - Ingresar:
     - Endpoint (ej: `localhost:8080`)
     - Access Key ID
     - Secret Access Key
     - Usar SSL/TLS (Sí/No)

4. **Montar buckets**
   - Click en el icono
   - Ir a "Buckets" → Seleccionar bucket
   - Ingresar letra de unidad (Windows: `M:`) o ruta (Linux: `/mnt/bucket`)
   - ¡El bucket aparecerá como unidad en tu sistema!

5. **Desmontar**
   - Click en el bucket montado para desmontarlo

## Configuración

La configuración se guarda automáticamente en:
- **Windows**: `C:\Users\<usuario>\.maxiofs-agent\config.json`
- **Linux/Mac**: `~/.maxiofs-agent/config.json`

## Características Técnicas

- Backend: Go + AWS SDK for Go v2
- Filesystem virtual: cgofuse (WinFsp/FUSE)
- Interfaz: systray + diálogos nativos
- Sin dependencias web (ejecutable nativo)
- Tamaño: ~13MB

## Compilar desde código

```bash
# Instalar dependencias
go mod download

# Compilar
export CGO_ENABLED=0
cd cmd/maxiofs-agent
go build -ldflags="-H windowsgui" -o ../../maxiofs-agent.exe
```

## Funcionalidades Futuras

- [ ] Caché local inteligente
- [ ] Sincronización bidireccional
- [ ] Notificaciones de cambios
- [ ] Estadísticas de uso
- [ ] Modo offline

## Licencia

MIT
