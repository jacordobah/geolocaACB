# ACB_GPS_Lo

Servicio Go de seguimiento técnico GPS con Redis. Se despliega sin Python ni MongoDB en su contenedor. **El binario actual sólo registra las rutas HTTP de salud y recepción de observaciones.** Las APIs de control y lectura están implementadas y probadas con verificadores aislados, pero no se registran en el proceso operativo hasta integrar un proveedor de identidad aprobado y una fuente de políticas autorizadas. El despacho a Operaciones también está deshabilitado por defecto.

## Documentación para integración

- [Arquitectura y estado](docs/arquitectura.md)
- [OpenAPI de handlers implementados y su estado operativo](docs/openapi.yaml)
- [Matriz de habilitación y prerrequisitos operativos](docs/matriz-habilitacion.md)
- [Dependencias entre equipos](docs/dependencias.md)
- [Control administrativo](docs/control-administrativo.md)
- [Consulta autorizada de ubicaciones](docs/consulta-ubicaciones.md)
- [Presentación y observaciones GPS](docs/presentacion.md)
- [Eventos para Operaciones](docs/operaciones-eventos.md)
- [Contrato geográfico Python](docs/contrato-geografico.md)
- [Despliegue Docker](docs/despliegue.md)
- [Pruebas y ejemplo aislado](docs/pruebas-integracion.md)
- [Limitaciones y decisiones pendientes](docs/limitaciones.md)

La documentación es local para revisión; no se ha preparado ni publicado en Git.
