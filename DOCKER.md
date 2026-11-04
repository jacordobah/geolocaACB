# Ejecucion con Docker

Desde la raiz del repositorio, inicia MongoDB, la API FastAPI y el servicio GPS
(con su propio Redis) con:

```sh
docker compose up --build
```

Servicios publicados en el host:

- API de rutas y puntos de encuentro: <http://localhost:8000>, documentacion
  interactiva en <http://localhost:8000/docs>.
- GPS: <http://localhost:8079>, con `GET /health/live` y `GET /health/ready`.
- MongoDB: `localhost:27017`; sus datos se conservan en el volumen `mongo_data`.

GPS mantiene su Redis en la red privada `gps-net`; no publica ese puerto al
host. Sus datos se conservan en el volumen `gps-redis-data`. El contenedor GPS
tambien se conecta a la red externa `acb-net` con el alias `geoloca-gps`, para
que el API Gateway pueda alcanzarlo cuando tenga configuradas sus rutas.

El control administrativo, la lectura de posiciones y la entrega de eventos a
Operaciones siguen deshabilitados por defecto. Para habilitarlos, defina las
variables correspondientes en el entorno o en un `.env` local; consulte
`acb_gps_lo/.env.example` y `acb_gps_lo/docs/despliegue.md`.

Para importar un KMZ:

```sh
curl -F "file=@ruta.kmz" http://localhost:8000/api/v1/imports/kmz
```

La respuesta indica cuantas rutas y puntos se guardaron, junto con el
identificador de la importacion.

Para detener los contenedores sin borrar los datos:

```sh
docker compose down
```

Para borrar tambien los datos persistidos:

```sh
docker compose down --volumes
```

Los Dockerfiles y las dependencias estan en `acb_path_lo/` y `acb_gps_lo/`.
Compose usa cada carpeta como contexto de construccion. El backend de rutas
ejecuta `acb_path_lo.main:app`; GPS ejecuta el binario Go `acb-gps`.

El backend de rutas espera que MongoDB este disponible al iniciar. La API
procesa todos los archivos KML incluidos en el KMZ y guarda cada recorrido en
la coleccion `routes`, con sus coordenadas en geometria GeoJSON `LineString`.
Guarda cada punto en `meeting_points`, con su ubicacion GeoJSON `Point`. Ambos
tipos incluyen el identificador de importacion para relacionar los documentos.

El componente GPS tambien puede desplegarse de forma independiente desde
`acb_gps_lo/` con `docker compose -f compose.yaml up -d`.
