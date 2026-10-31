# Configuración local del primer punto de encuentro

La herramienta recibe identificadores verificados por el operador y no deduce
relaciones por nombres, distancia ni orden. Desde la importación KMZ, las rutas 541
resueltas de la rama `RUTAS AL COLEGIO EN BICI` reciben automáticamente
`start_meeting_point_id` y `first_meeting_point_id`; la herramienta queda para
verificar o completar asociaciones en rutas no resueltas y para repetir una
configuración idéntica. No existe un endpoint HTTP de escritura. Los casos de uso
reciben cadenas y devuelven un modelo de dominio sin BSON.

El importador persiste además `start_meeting_point_id` y `end_meeting_point_id`
como ObjectId para las rutas resueltas. La herramienta solo modifica
`first_meeting_point_id`.

Antes de usarla, verificar mediante `docker inspect` que el contenedor pertenece a
la red `acb-kmz-test-20261012_default` y que MongoDB monta exclusivamente el volumen
de pruebas en `/data/db`. La variable `ACB_TEST_PROJECT` y `--confirm` evitan usos
accidentales; no sustituyen la comprobación del entorno ni son autenticación.

Dentro del contenedor temporal conectado a esa red, con `MONGO_URI` igual a
`mongodb://acb_mongo_db:27017`, `MONGO_DATABASE=geolocaacb` y
`ACB_TEST_PROJECT=acb-kmz-test-20261012`:

```powershell
docker --context desktop-linux exec acb-rf09-phase1-preview python -B -m acb_path_lo.infrastructure.tools.configure_first_meeting_point --route-id $RouteId --point-id $PointId --confirm
```

La herramienta verifica existencia y geometría del punto. La actualización
condicional de la ruta es atómica, sin upsert. Acepta una referencia ausente o
idéntica; rechaza una diferente. Una referencia explícita nula también requiere
revisión y no se reemplaza automáticamente.

MongoDB standalone no admite una transacción entre los dos documentos. La
existencia del punto se verifica antes de la actualización; una eliminación o
modificación concurrente externa del punto no queda protegida. Para este
prototipo no deben ejecutarse esas operaciones concurrentes. La lectura también
verifica existencia y geometría, y devuelve un conflicto si la referencia deja de
ser válida. Una garantía transaccional futura requeriría un despliegue compatible
con transacciones; esta fase no cambia MongoDB.

La consulta pública de lectura es:

```text
GET /api/v1/routes/{route_id}/first-meeting-point
```

La verificación utiliza una API temporal en `127.0.0.1:18000`, con código montado
en lectura desde el workspace y la imagen existente. No recrea la API de Compose
en el puerto 8000. Tras retirar el contenedor temporal, el código nuevo necesita
un despliegue explícito para quedar disponible en el servicio permanente.

Las pruebas nuevas usan unittest y dobles en memoria. La integración real es
optativa, solo de lectura, y requiere `ACB_INTEGRATION_URL`,
`ACB_INTEGRATION_ROUTE_ID`, `ACB_INTEGRATION_POINT_ID`, `MONGO_URI` y
`MONGO_DATABASE`. Dentro de la API temporal, la URL es `http://localhost:8000`;
desde Windows es `http://localhost:18000`.

