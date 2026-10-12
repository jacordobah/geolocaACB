from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from fastapi.routing import APIRoute


class JsonSafeValidationRoute(APIRoute):
    """Return 422 even when invalid input contains non-JSON NaN/Infinity tokens."""

    def get_route_handler(self):
        handler = super().get_route_handler()

        async def validate(request):
            try:
                return await handler(request)
            except RequestValidationError as error:
                # Do not echo input/ctx: they can themselves be non-JSON values.
                details = [{key: item[key] for key in ("type", "loc", "msg")}
                           for item in error.errors()]
                return JSONResponse(status_code=422, content={"detail": details})

        return validate
