# ACB_GPS_Lo

Servicio Go de seguimiento técnico GPS con Redis. Se despliega sin Python ni MongoDB en su contenedor. El binario registra salud y recepción de observaciones; **control y lectura sólo se registran cuando se habilitan explícitamente con catálogo, rutas Gateway y credencial interna válidos**. La autorización contextual de recorridos y políticas se delega al Gateway confiable en el Prototipo 2. El despacho a Operaciones permanece deshabilitado por defecto.

**Regla de integración:** toda comunicación HTTP con otros componentes pasa por el API Gateway. Presentación y el futuro controlador administrativo acceden a GPS a través de él; GPS consulta las rutas de Python y envía eventos a Operaciones usando exclusivamente rutas configuradas del Gateway. Redis y los recursos locales siguen siendo dependencias privadas de GPS. La ruta, identidad de servicio, red y semántica de acuse deben acordarse con el equipo Gateway antes de habilitar integraciones reales.

## Documentación para integración

- [Arquitectura y estado](docs/arquitectura.md)
- [OpenAPI de handlers implementados y su estado operativo](docs/openapi.yaml)
- [Matriz de habilitación y prerrequisitos operativos](docs/matriz-habilitacion.md)
- [Catálogo local de políticas YAML](docs/politicas-yaml.md)
- [Dependencias entre equipos](docs/dependencias.md)
- [Control administrativo](docs/control-administrativo.md)
- [Contrato interno para el API Gateway](docs/contrato-gateway-gps.md)
- [Consulta autorizada de ubicaciones](docs/consulta-ubicaciones.md)
- [Presentación y observaciones GPS](docs/presentacion.md)
- [Eventos para Operaciones](docs/operaciones-eventos.md)
- [Contrato geográfico Python](docs/contrato-geografico.md)
- [Despliegue Docker](docs/despliegue.md)
- [Pruebas y ejemplo aislado](docs/pruebas-integracion.md)
- [Limitaciones y decisiones pendientes](docs/limitaciones.md)

La documentación de este cambio queda en el árbol de trabajo para revisión; no se realizaron commits ni push.
