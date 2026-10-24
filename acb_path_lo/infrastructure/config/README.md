# RF-09: configuración de validación geográfica

El POST `/api/v1/routes/{route_id}/first-meeting-point/validate` calcula una
distancia y una evaluación geográfica. No registra llegada ni guarda posiciones.

Se requieren `ACB_FIRST_PE_RADIUS_M` y `ACB_FIRST_PE_MAX_ACCURACY_M`, en metros,
positivos y finitos. No hay valores funcionales predeterminados. La política se
carga una vez durante el lifespan de la instancia; cambiar variables después no
altera la política hasta un nuevo arranque. Configuración ausente o inválida
deshabilita solo este POST con 503 y `geofence_configuration_unavailable`.

La entrada admite números JSON estrictos para `latitude`, `longitude` y
`accuracy_m` opcional. Precisión presente debe ser positiva y finita. Se rechazan
campos desconocidos, radio del cliente, booleanos, cadenas numéricas y valores no
finitos. `captured_at` opcional debe ser un timestamp ISO con zona. Se conserva en
UTC como información del dispositivo; no se comprueba antigüedad. `validated_at`
es un instante UTC del servidor, no una hora de llegada.

Haversine usa una esfera de radio 6.371.000 m; no calcula longitud por calles,
altitud ni distancia elipsoidal. Las coordenadas se consideran WGS84.

Se calcula `point_inside = distance_m <= radius_m` sin redondeo previo. Con
precisión horizontal reportada `a`, el intervalo aproximado es
`[max(0, distance_m-a), distance_m+a]`. Con precisión aceptable:

- `inside` si el extremo superior es menor o igual que el radio;
- `outside` si el extremo inferior es mayor que el radio;
- `indeterminate` en los demás casos.

Sin precisión o si supera el máximo configurado, el estado es `indeterminate`.
Estos intervalos dependen de la incertidumbre reportada: no son garantías
estadísticas ni pruebas definitivas de presencia física. `inside` no significa
llegada confirmada. No incluyen incertidumbre cartográfica del PE ni error del
modelo esférico. El contrato del dispositivo debe confirmar que `accuracy_m`
representa un radio de incertidumbre horizontal.

Las APIs temporales de prueba usan radio 30 m y precisión máxima 10 m solo como
valores de ensayo, no como decisiones funcionales. No se modifica Compose.

Las pruebas unitarias se ejecutan con unittest. La integración real requiere
`ACB_PHASE2_URL`, `ACB_PHASE2_UNCONFIGURED_URL`, `ACB_PHASE2_INVALID_URL`,
`ACB_INTEGRATION_ROUTE_ID`, las dos variables de política y la configuración de
MongoDB. Solo lee MongoDB, verifica HTTP y compara todos los documentos e índices
antes y después. No configura asociaciones ni inserta fixtures.

## Tolerancias de extremos de ruta

La importación KMZ resuelve inicio y fin de las rutas 541/542 desde los puntos de
encuentro y colegios. La política se carga una vez por lifespan en
`app.state.endpoint_policy` desde:

- `ACB_ENDPOINT_PE_TOLERANCE_M` (150 m por defecto): distancia de referencia entre
  un vértice extremo y el punto de encuentro elegido por numeración para el mismo
  código de ruta. Un `PE` numerado fuera de este umbral se usa igualmente, pero con
  `confidence: low`.
- `ACB_ENDPOINT_SCHOOL_TOLERANCE_M` (120 m por defecto): distancia máxima entre un
  vértice extremo y un colegio. Fuera de este umbral el colegio no se asocia.

Valores ausentes, no finitos o no positivos usan el valor por defecto con un
warning. Los puntos explícitos de inicio y fin dentro de la carpeta de la ruta
tienen prioridad sobre la inferencia.

La inferencia usa la numeración de los puntos de encuentro: en rutas `541` el
inicio es el `PE` de menor número y el fin el colegio; en rutas `542` el inicio es
el colegio y el fin el `PE` de mayor número. La geometría solo determina el lado del
colegio, la variante (`542`/`542 B`) y las distancias. Si no hay `PE` numerados se
mantiene el emparejamiento geométrico de respaldo.
