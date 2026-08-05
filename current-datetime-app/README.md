# Current Datetime Spring Boot App

A minimal Spring Boot 4.1 application that returns the current UTC date and time as JSON.

## Requirements

- Java 17 or newer
- Maven 3.6.3 or newer

## Run

The supplied ZIP includes a prebuilt executable JAR:

```bash
java -jar current-datetime-app.jar
```

To build the project from source instead:

```bash
mvn spring-boot:run
```

Or build and run the executable JAR:

```bash
mvn clean package
java -jar target/current-datetime-app-0.0.1-SNAPSHOT.jar
```

The application listens on port `8080`.

```bash
curl http://localhost:8080/datetime
```

Example response:

```json
{
  "datetime": "2026-08-05T12:34:56.123456Z",
  "timezone": "UTC"
}
```

Both `/` and `/datetime` return the same response.

## Expose with Mockingo

From the extracted application directory:

```bash
mockingo expose \
  --name datetime-demo \
  --http 8080 \
  -- java -jar current-datetime-app.jar
```
