package com.mockingo.examples.datetime;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;

import org.junit.jupiter.api.Test;

class DateTimeControllerTest {

    @Test
    void returnsCurrentUtcDateTime() {
        Clock clock = Clock.fixed(Instant.parse("2026-08-05T12:34:56Z"), ZoneOffset.UTC);
        DateTimeController controller = new DateTimeController(clock);

        DateTimeController.DateTimeResponse response = controller.currentDateTime();

        assertThat(response.datetime()).isEqualTo("2026-08-05T12:34:56Z");
        assertThat(response.timezone()).isEqualTo("UTC");
    }
}
