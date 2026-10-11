# Ejecucion con Docker

Desde la raiz del repositorio, inicia MongoDB y la API FastAPI con:

```sh
docker compose up --build
```

La API queda disponible en <http://localhost:8000> y su documentacion
interactiva en <http://localhost:8000/docs>. MongoDB queda disponible en
`localhost:27017`; sus datos se conservan en el volumen `mongo_data`.

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

El Dockerfile y las dependencias del backend estan en `acb_path_lo/`.
Compose usa esa carpeta como contexto de construccion y ejecuta
`acb_path_lo.main:app`.
El backend espera que MongoDB este disponible al iniciar. La API procesa todos
los archivos KML incluidos en el KMZ y guarda cada recorrido en la coleccion
`routes`, con sus coordenadas en geometria GeoJSON `LineString`. Guarda cada
punto en `meeting_points`, con su ubicacion GeoJSON `Point`. Ambos tipos
incluyen el identificador de importacion para relacionar los documentos.
